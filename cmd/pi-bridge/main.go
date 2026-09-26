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
	"strings"
	"syscall"
	"time"

	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/observe"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/transport"
	"pi-bridge-go/internal/tunnel"
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
	relayURL := flag.String("relay", "", "云端转发器地址，例如 wss://relay.example.com；为空表示仅本地")
	deviceID := flag.String("device-id", "", "设备标识；启用 --relay 时必填")
	deviceName := flag.String("device-name", "", "设备显示名，随配对信息一起登记")
	root := flag.String("workspace", "", "必填：允许的工作区根目录")
	binary := flag.String("pi", "pi", "Pi 可执行文件路径")
	stateDir := flag.String("state-dir", filepath.Join(cache, "pi-bridge-go"), "桥自有的运行目录")
	agentDir := flag.String("agent-dir", "", "Pi 配置目录，缺省使用隔离的 state-dir/agent")
	extensions := flag.Bool("extensions", false, "加载 Pi 已配置资源；项目信任仍保持拒绝")
	idle := flag.Duration("idle-timeout", 2*time.Minute, "空闲工作进程的回收时间")
	maxWorkers := flag.Int("max-workers", 4, "活跃工作进程上限")
	maxTerminals := flag.Int("max-terminals", 4, "并发终端上限")
	terminalIdle := flag.Duration("terminal-idle", 10*time.Minute, "空闲终端的回收时间")
	flag.Parse()

	if *root == "" {
		return errors.New("必须指定 --workspace")
	}
	if *relayURL != "" && *deviceID == "" {
		return errors.New("启用 --relay 时必须提供 --device-id")
	}
	if *relayURL != "" && !strings.HasPrefix(*relayURL, "ws://") && !strings.HasPrefix(*relayURL, "wss://") {
		return errors.New("--relay 必须以 ws:// 或 wss:// 开头")
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
	exportDir := filepath.Join(absolute, "exports")
	for _, dir := range []string{absolute, sessionDir, *agentDir, exportDir} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}

	store, err := sessions.New(sessionDir, policy, sessions.DefaultLimits())
	if err != nil {
		return err
	}
	defer store.Close()

	receipts, err := storage.NewReceipts(filepath.Join(absolute, "receipts"), storage.DefaultLimits())
	if err != nil {
		return err
	}
	defer receipts.Close()

	// 方法集合与能力清单同源，避免新增命令时漏登记指标。
	metrics := observe.NewMetrics(observe.NewMethods(transport.SupportedMethods...))

	cfg := run.Defaults()
	cfg.Binary = *binary
	cfg.AgentDir = *agentDir
	cfg.Store = store
	cfg.Policy = policy
	cfg.Extensions = *extensions
	cfg.IdleTimeout = *idle
	cfg.MaxWorkers = *maxWorkers
	cfg.Metrics = metrics
	files, err := workspace.NewFiles(policy, workspace.DefaultLimits())
	if err != nil {
		return err
	}
	defer files.Close()

	termCfg := terminal.Defaults()
	termCfg.MaxTerminals = *maxTerminals
	termCfg.IdleTimeout = *terminalIdle
	terminals := terminal.NewManager(termCfg)
	defer terminals.Close()

	manager := run.New(cfg)
	defer manager.Close()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	piConfig := management.NewConfig(*agentDir, management.DefaultLimits())
	handler := transport.New(manager, store, terminals, files, piConfig, exportDir, receipts, metrics, token, ln.Addr().String())

	// 云端隧道：本地主动外连，relay 只搬运字节。
	var tunnelClient *tunnel.Client
	var bridge *transport.TunnelBridge
	if *relayURL != "" {
		deviceToken := os.Getenv("PI_BRIDGE_DEVICE_TOKEN")
		if deviceToken == "" {
			return errors.New("启用 --relay 时必须设置 PI_BRIDGE_DEVICE_TOKEN")
		}
		bridge = transport.NewTunnelBridge(handler, nil, 8, 5*time.Minute)
		handler.SetTunnelBridge(bridge)
		cfg := tunnel.Defaults()
		cfg.RelayURL = *relayURL
		cfg.DeviceID = *deviceID
		cfg.DeviceToken = deviceToken
		tunnelClient = tunnel.NewClient(cfg, bridge)
		bridge.SetSender(tunnelClient.Send)
		go tunnelClient.Run(context.Background())
		slog.Info("已启用云端隧道", "转发器", *relayURL, "设备", *deviceID, "设备名", *deviceName)
	}
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
	// 关闭顺序：先隧道接入层（取消虚拟连接），再管理器（连带取消已升级的 WS 连接），
	// 最后才是 HTTP Shutdown——Shutdown 本身不负责已升级的连接。
	if bridge != nil {
		bridge.Close()
	}
	manager.Close()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return server.Shutdown(ctx)
}
