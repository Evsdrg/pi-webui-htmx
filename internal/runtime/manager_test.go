package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/workspace"
)

// newTestManager 用假 Pi 可执行文件构造管理器，测试绝不调用真实模型。
func newTestManager(t *testing.T, opts ...func(*Config)) (*Manager, string) {
	t.Helper()
	cwd := t.TempDir()
	state := t.TempDir()
	sessionDir := filepath.Join(state, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(state, "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspace.New([]string{cwd})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessions.New(sessionDir, policy, sessions.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fakePi, err := filepath.Abs(filepath.Join("..", "..", "testdata", "bin", "fake-pi"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	cfg.Binary = fakePi
	cfg.AgentDir = agentDir
	cfg.Store = store
	cfg.Policy = policy
	cfg.MaxWorkers = 2
	cfg.IdleTimeout = 150 * time.Millisecond
	cfg.StopGrace = 200 * time.Millisecond
	cfg.StartTimeout = 5 * time.Second
	cfg.OperationTimeout = 2 * time.Second
	for _, opt := range opts {
		opt(&cfg)
	}
	m := New(cfg)
	t.Cleanup(m.Close)
	return m, cwd
}

func TestStart握手并复用同一进程(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w1, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if w1.Info().SessionID != "fake-session" || w1.Info().Status != "idle" {
		t.Fatalf("握手结果异常: %+v", w1.Info())
	}
	w2, err := m.Start(ctx, "fake-session", cwd)
	if err != nil {
		t.Fatalf("复用失败: %v", err)
	}
	if w1 != w2 {
		t.Fatal("同一会话应复用同一工作进程")
	}
	if len(m.List()) != 1 {
		t.Fatalf("进程表应只有一项: %+v", m.List())
	}
}

func TestPrompt返回接受并推送事件(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, info, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if info.Epoch == "" {
		t.Fatal("订阅应返回 epoch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Prompt(ctx, "你好", ""); err != nil {
		t.Fatalf("发送提示词失败: %v", err)
	}
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for len(seen) < 3 && time.Now().Before(deadline) {
		msg, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if msg.Event != "pi.event" {
			continue
		}
		raw, _ := json.Marshal(msg.Data)
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &ev)
		seen[ev.Type] = true
	}
	if !seen["agent_start"] || !seen["message_update"] || !seen["agent_settled"] {
		t.Fatalf("事件不完整: %v", seen)
	}
}

func TestAbort先清队列(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Abort(ctx); err != nil {
		t.Fatalf("中止失败: %v", err)
	}
	if w.Info().Busy {
		t.Fatal("中止后不应仍处于忙状态")
	}
}

func Test空闲后自动回收(t *testing.T) {
	m, cwd := newTestManager(t)
	if _, err := m.Start(context.Background(), "", cwd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.List()) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("空闲工作进程未被回收: %+v", m.List())
}

func Test历史查询不启动工作进程(t *testing.T) {
	m, cwd := newTestManager(t)
	if w, err := m.Get("fake-session"); err == nil {
		t.Fatalf("未启动时不应返回工作进程: %+v", w)
	}
	if len(m.List()) != 0 {
		t.Fatal("查询不应产生工作进程")
	}
	_ = cwd
}

func TestGet未启动返回明确错误(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Get("fake-session")
	if err == nil {
		t.Fatal("未启动会话应报错")
	}
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "worker_not_running" {
		t.Fatalf("错误码应为 worker_not_running，实际 %v", err)
	}
}

func Test扩展对话被显式取消(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.Env = []string{"FAKE_PI_SCRIPT=extension_dialog"} })
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Prompt(ctx, "触发对话", "") }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if msg.Event != "pi.event" {
			continue
		}
		raw, _ := json.Marshal(msg.Data)
		var ev struct {
			Type   string `json:"type"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &ev)
		if ev.Type == "extension_ui_request" && ev.Method == "confirm" {
			return
		}
	}
	t.Fatal("未收到扩展对话请求")
}

func Test并发订阅不阻塞事件(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		sub, _, err := w.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sub.Close()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				_, _ = sub.Next(ctx)
				cancel()
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		if err := w.Prompt(ctx, "压测", ""); err != nil {
			t.Fatalf("慢订阅者不应阻塞发送: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestStop后进程真正退出(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	pid := w.Info().PID
	if err := w.Stop(true); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if processAlive(pid) {
		t.Fatal("停止后进程仍存活")
	}
	// 进程表由退出协程清理，允许极短收敛时间，但不应长期残留。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.Get("fake-session"); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("停止后进程表仍残留该会话")
}

func TestPi异常退出后状态可见(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.Env = []string{"FAKE_PI_SCRIPT=crash"} })
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if msg.Event == "bridge.worker_state" {
			data, _ := json.Marshal(msg.Data)
			var info Info
			_ = json.Unmarshal(data, &info)
			if info.Status == "failed" || info.Status == "stopped" {
				return
			}
		}
	}
	t.Fatal("未收到进程退出状态")
}
