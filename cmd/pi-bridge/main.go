// Command pi-bridge 是本地桥入口：管理 Pi 工作进程，提供受控的 HTTP/WS 接口。
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

	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/transport"
	"pi-bridge-go/internal/workspace"
)

func main() {
	if err := serve(); err != nil {
		slog.Error("桥已停止", "错误", err)
		os.Exit(1)
	}
}

func serve() error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	listen := flag.String("listen", "127.0.0.1:30142", "仅接受环回地址的监听地址")
	root := flag.String("workspace", "", "必填：允许的工作区根目录")
	binary := flag.String("pi", "pi", "Pi 可执行文件路径")
	stateDir := flag.String("state-dir", filepath.Join(cache, "pi-bridge-go"), "桥自有的运行目录")
	agentDir := flag.String("agent-dir", "", "Pi 配置目录，缺省使用隔离的 state-dir/agent")
	extensions := flag.Bool("extensions", false, "加载 Pi 已配置资源；项目信任仍保持拒绝")
	idle := flag.Duration("idle-timeout", 2*time.Minute, "空闲工作进程的回收时间")
	maxWorkers := flag.Int("max-workers", 4, "活跃工作进程上限")
	flag.Parse()

	if *root == "" {
		return errors.New("必须指定 --workspace")
	}
	token := os.Getenv("PI_BRIDGE_TOKEN")
	if len(token) < 32 {
		return errors.New("请设置 PI_BRIDGE_TOKEN，至少 32 个随机字符")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("A 阶段只接受环回 IP")
	}
	if *idle < time.Second || *maxWorkers < 1 || *maxWorkers > 32 {
		return errors.New("空闲超时或工作进程上限取值无效")
	}

	policy, err := workspace.New([]string{*root})
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	sessionDir := filepath.Join(absolute, "sessions")
	if *agentDir == "" {
		*agentDir = filepath.Join(absolute, "agent")
	} else {
		*agentDir, err = filepath.Abs(*agentDir)
		if err != nil {
			return err
		}
	}
	for _, dir := range []string{absolute, sessionDir, *agentDir} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}

	store, err := sessions.New(sessionDir, policy, sessions.DefaultLimits())
	if err != nil {
		return err
	}
	defer store.Close()

	cfg := run.Defaults()
	cfg.Binary = *binary
	cfg.AgentDir = *agentDir
	cfg.Store = store
	cfg.Policy = policy
	cfg.Extensions = *extensions
	cfg.IdleTimeout = *idle
	cfg.MaxWorkers = *maxWorkers
	manager := run.New(cfg)
	defer manager.Close()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	handler := transport.New(manager, store, token, ln.Addr().String())
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}

	sigctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	slog.Info("桥已监听", "地址", "http://"+ln.Addr().String(), "阶段", "A", "pi", *binary)

	select {
	case <-sigctx.Done():
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务：%w", err)
		}
	}
	// 关闭管理器会连带取消已升级的 WS 连接，Shutdown 本身不负责这部分。
	manager.Close()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return server.Shutdown(ctx)
}
