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

func serve() error {
	listen := flag.String("listen", "127.0.0.1:30143", "监听地址")
	stateDir := flag.String("state-dir", filepath.Join(os.TempDir(), "pi-relay"), "转发器自有状态目录")
	host := flag.String("host", "", "期望的 Host；为空表示不校验（仅限本机测试）")
	addUser := flag.String("add-user", "", "添加用户并打印一次性令牌")
	addDevice := flag.String("add-device", "", "为设备登记预共享密钥并打印一次性密钥")
	flag.Parse()

	if err := os.MkdirAll(*stateDir, 0700); err != nil {
		return err
	}
	secret := os.Getenv("PI_RELAY_SECRET")
	if len(secret) < 32 {
		return errors.New("请设置 PI_RELAY_SECRET，至少 32 个随机字符")
	}
	users, err := relay.NewUsers(secret)
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
	registry, err := relay.NewRegistry(*stateDir, relay.DefaultLimits())
	if err != nil {
		return err
	}
	defer registry.Close()

	handler := relay.NewServer(registry, users, *host)
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
