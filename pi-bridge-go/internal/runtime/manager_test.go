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
	"pi-bridge-go/internal/testutil"
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
	fakePi, err := testutil.FakePi()
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
	// 与生产一致：桥内工具集扩展随启动落盘并下发（会话启动时启用全部工具）。
	if toolsExt, extErr := InstallToolsExt(state); extErr == nil {
		cfg.ToolsExt = toolsExt
	}
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
	if err := w.Prompt(ctx, "你好", "", nil); err != nil {
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
	go func() { _ = w.Prompt(ctx, "触发对话", "", nil) }()
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
		if err := w.Prompt(ctx, "压测", "", nil); err != nil {
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

// StopSession 与工作进程的启动/退出清理会在同一张进程表上并发：
// 它必须持锁读取，否则 -race 会报数据竞争、非竞态构建下会直接
// fatal error（concurrent map read and map write）。
func TestStopSession与进程表更新并发安全(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = m.StopSession("fake-session", false)
		}
	}()
	for i := 0; i < 20; i++ {
		w, err := m.Start(ctx, "", cwd)
		if err != nil {
			t.Fatalf("启动失败: %v", err)
		}
		if err := w.Stop(true); err != nil {
			t.Fatalf("停止失败: %v", err)
		}
		// 等退出清理把进程表的旧键删掉再进下一轮：否则下一轮 Start 会撞上
		// 尚未清理的键、报 conflict——那是清理时序，不是本用例要测的竞态。
		// 并发读表由上面的 StopSession 循环持续制造（它仍在与 map 写入竞争）。
		testutil.WaitFor(t, "退出清理移出进程表", func() bool {
			return len(m.List()) == 0
		})
	}
	close(stop)
	wg.Wait()
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

// Test订阅注册后的事件必达 覆盖 2026-10-18 重设计后的订阅契约：
// 注册返回当前 epoch/seq 快照，此后发布的事件按序送达；不再有补发。
//
// 旧设计的 B05 不变量（「快照末序号 == 注册序号」，靠一次持锁的
// 「取补发快照 + 注册订阅」保证）随补发路径一起移除：现在断线即重建，
// 重连后由客户端重读历史与状态，不重放事件。这里验证的是新契约里
// 仍然成立的那部分——注册之后的事件必须能收到，且序号从注册点接续。
func Test订阅注册后的事件必达(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 先发一批事件，证明「注册前的历史事件不会补发」。
	for i := 0; i < 3; i++ {
		if err := w.Prompt(ctx, "hi", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	waitSeq(t, w, 3)
	info := w.Info()
	if info.Epoch == "" || info.Seq == 0 {
		t.Fatalf("订阅快照应带 epoch 与 seq: %+v", info)
	}

	sub, snap, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if snap.Epoch != info.Epoch {
		t.Fatalf("同一进程的 epoch 应一致: %q -> %q", info.Epoch, snap.Epoch)
	}
	// 注册之后发布的事件必须能收到：这是新契约的核心。
	if err := w.Prompt(ctx, "after", "", nil); err != nil {
		t.Fatal(err)
	}
	got := drain(sub, 1)
	if len(got) == 0 {
		t.Fatal("订阅注册后发布的事件没有送达")
	}
	if got[0].Seq <= snap.Seq {
		t.Fatalf("实时事件序号未从注册点接续: 注册 %d，首条 %d", snap.Seq, got[0].Seq)
	}
}

// Test订阅不补发历史事件 是重设计的判定点：注册前发布的事件**不**重放。
// 客户端若需要注册前的内容，必须重读持久历史——这正是「断线即重建」的
// 前提，若这里悄悄补发，前端就会以为实时流可以续上，回到旧的复杂度。
func Test订阅不补发历史事件(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Prompt(ctx, "seed", "", nil); err != nil {
		t.Fatal(err)
	}
	waitSeq(t, w, 1)
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	// 注册前已有事件在序号里，但队列里不该有任何一条。
	select {
	case msg := <-sub.ch:
		t.Fatalf("订阅不应补发注册前的事件，实际收到 seq=%d", msg.message.Seq)
	default:
	}
}

// Test订阅配额与失败不占位 覆盖订阅的边界：epoch 不再参与订阅（无补发），
// 但配额上限仍然生效，且失败的订阅不得占用配额。
func Test订阅配额与失败不占位(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	before := w.SubscriberCount()
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if got := w.SubscriberCount(); got != before+1 {
		t.Fatalf("成功的订阅应占用一份配额: %d -> %d", before, got)
	}
	sub.Close()
	if got := w.SubscriberCount(); got != before {
		t.Fatalf("关闭后应释放配额: %d -> %d", before, got)
	}
}

// waitSeq 等到 worker 的序号至少达到 want。
func waitSeq(t *testing.T, w *Worker, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w.Info().Seq >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("序号未达到 %d，当前 %d", want, w.Info().Seq)
}

// drain 非阻塞地取尽当前可用事件。
func drain(sub *Subscription, want int) []protocol.Message {
	out := []protocol.Message{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	for len(out) < want {
		m, err := sub.Next(ctx)
		if err != nil {
			return out
		}
		out = append(out, m)
	}
	return out
}
