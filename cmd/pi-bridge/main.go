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
	"pi-bridge-go/internal/presentation"
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
	listen := flag.String("listen", "127.0.0.1:30142", "监听地址：环回地址，或私有/overlay 网段地址（后者必须同时指定 --public-origin）")
	publicOriginRaw := flag.String("public-origin", "", "对外访问来源，例如 https://example.com:39080；声明后 Host/Origin 也接受它，Cookie 的 Secure 跟随其 scheme")
	relayURL := flag.String("relay", "", "云端转发器地址，例如 wss://relay.example.com；为空表示仅本地")
	deviceID := flag.String("device-id", "", "设备标识；启用 --relay 时必填")
	deviceName := flag.String("device-name", "", "设备显示名，随配对信息一起登记")
	root := flag.String("workspace", "", "必填：允许的工作区根目录")
	binary := flag.String("pi", "pi", "Pi 可执行文件路径")
	stateDir := flag.String("state-dir", filepath.Join(cache, "pi-bridge-go"), "桥自有的运行目录")
	agentDir := flag.String("agent-dir", "", "Pi 配置目录，缺省使用隔离的 state-dir/agent")
	extensions := flag.Bool("extensions", false, "加载 Pi 已配置资源；项目信任仍保持拒绝")
	uiDir := flag.String("ui-dir", "", "pi-webui-htmx 检出目录；为空则禁用 UI 层，只提供 JSON/WS API")
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
	publicOrigin, err := transport.ParsePublicOrigin(*publicOriginRaw)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("--listen 必须是 IP:端口")
	}
	if !ip.IsLoopback() {
		// 非环回监听等于把桥暴露给隧道/局域网对端，因此要求两件事：
		// 地址本身不是可路由到公网的地址，且部署者显式声明对外来源。
		if !privateOrOverlay(ip) {
			return errors.New("--listen 只接受环回、私有网段或 overlay 网段地址")
		}
		if !publicOrigin.Enabled() {
			return errors.New("绑定非环回地址时必须显式指定 --public-origin")
		}
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
	var ui *presentation.Renderer
	if *uiDir != "" {
		rendered, err := presentation.LoadFromDir(*uiDir, transport.SupportedMethods...)
		if err != nil {
			return fmt.Errorf("加载 UI 包失败：%w", err)
		}
		ui = rendered
		slog.Info("已加载 UI 包", "目录", *uiDir, "模板数", len(ui.TemplateNames()))
	} else {
		slog.Info("未指定 --ui-dir，UI 层禁用")
	}

	piConfig := management.NewConfig(*agentDir, management.DefaultLimits())
	handler, err := transport.New(transport.Options{
		Manager: manager, Store: store, Terminals: terminals, Files: files,
		Config: piConfig, Discovery: management.DefaultDiscoveryLimits(),
		ExportDir: exportDir, Receipts: receipts, Metrics: metrics,
		Token: token, Host: ln.Addr().String(), PublicOrigin: publicOrigin,
		UI: ui, WorkspaceRoot: *root,
	})
	if err != nil {
		return err
	}

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

// privateOrOverlay 判断监听地址是否属于「不会路由到公网」的网段：
// RFC1918 私有地址、IPv6 ULA、链路本地，以及 RFC6598 的 100.64.0.0/10
// （运营商级 NAT，Tailscale / EasyTier 这类 overlay 常用）。
// 桥一旦绑定这类地址，对端就是隧道/局域网里的设备，而不是整个互联网。
func privateOrOverlay(ip net.IP) bool {
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1]&0xc0 == 64
	}
	return false
}
