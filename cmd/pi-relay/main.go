// Command pi-relay 是云端转发器：把浏览器连接路由到本地桥的主动隧道。
// 它不运行 Pi、不落盘会话正文、不接触模型密钥。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pi-bridge-go/internal/relay"
)

func main() {
	if err := serve(); err != nil {
		slog.Error("转发器已停止", "错误", err)
		os.Exit(1)
	}
}

// defaultStateDir 返回用户持久目录下的状态路径。
// Go 标准库没有 XDG_STATE_HOME 的 helper，这里与桥的 state-dir 同风格：
// 优先 $XDG_STATE_HOME，其次 ~/.local/state，最后才退回家目录。
func defaultStateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pi-relay")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pi-relay")
	}
	return filepath.Join(os.TempDir(), "pi-relay")
}

func serve() error {
	listen := flag.String("listen", "127.0.0.1:30143", "监听地址")
	// 默认放用户持久目录：设备注册表与用户表是**持久身份**，
	// 放系统临时目录会在重启或清理 /tmp 后整体消失（B55）。
	stateDir := flag.String("state-dir", defaultStateDir(), "转发器自有状态目录（持久；放临时目录会丢设备注册表）")
	host := flag.String("host", "", "必填：对外访问的精确 Host（含端口，例如 relay.example.com 或 1.2.3.4:30143）")
	addUser := flag.String("add-user", "", "添加用户并打印一次性令牌")
	addDevice := flag.String("add-device", "", "为设备登记预共享密钥并打印一次性密钥")
	flag.Parse()

	stateRoot, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		return err
	}
	// 显式把持久身份放进临时目录时明确警告：重启即失效。
	if tmp := os.TempDir(); tmp != "" {
		if rel, err := filepath.Rel(tmp, stateRoot); err == nil && !strings.HasPrefix(rel, "..") {
			slog.Warn("状态目录位于临时目录，重启或清理后设备注册表会丢失", "stateDir", stateRoot)
		}
	}
	secret := os.Getenv("PI_RELAY_SECRET")
	if len(secret) < 32 {
		return errors.New("请设置 PI_RELAY_SECRET，至少 32 个随机字符")
	}
	// 用户表落盘到 state-dir：--add-user/--add-device 是一次性 CLI 进程，
	// 不持久化的话服务进程重建后刚签发的凭据全部失效（B19）。
	users, err := relay.NewUsers(secret, filepath.Join(stateRoot, "users.json"))
	if err != nil {
		return err
	}

	// 一次性管理命令：加完即退，不在长驻进程里留后门。
	if *addUser != "" || *addDevice != "" {
		if *addUser != "" {
			token, err := users.AddUser(*addUser)
			if err != nil {
				return err
			}
			fmt.Println("用户:", *addUser)
			fmt.Println("令牌（仅显示一次）:", token)
		}
		if *addDevice != "" {
			deviceSecret, err := users.AddDeviceSecret(*addDevice)
			if err != nil {
				return err
			}
			fmt.Println("设备:", *addDevice)
			fmt.Println("预共享密钥（仅显示一次）:", deviceSecret)
		}
		return nil
	}

	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return err
	}
	registry, err := relay.NewRegistry(stateRoot, relay.DefaultLimits())
	if err != nil {
		return err
	}
	defer registry.Close()

	handler, err := relay.NewServer(registry, users, relay.Config{Host: *host})
	if err != nil {
		// 启动期就暴露，而不是运行期用 403 掩盖误配置（B63）。
		return fmt.Errorf("入口约束不完整: %w", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	sigctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	slog.Info("转发器已监听", "地址", "http://"+ln.Addr().String(), "设备注册表", registry.Stats())
	select {
	case <-sigctx.Done():
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务：%w", err)
		}
	}
	handler.Close()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return server.Shutdown(ctx)
}
