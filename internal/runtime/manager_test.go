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

// Test订阅与补发原子化 覆盖 B05：
// Replay() 与 Subscribe() 分成两次加锁时，两次锁之间发布的事件
// 既不在快照里也不会进入实时订阅，重连后静默丢失。
func Test订阅与补发原子化(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 先发一批事件，让补发环里有内容。
	for i := 0; i < 3; i++ {
		if err := w.Prompt(ctx, "hi", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	waitSeq(t, w, 3)
	info := w.Info()

	// 带游标订阅：必须同时拿到快照与已注册的订阅。
	sub, _, items, ok, err := w.SubscribeWithReplay(info.Epoch, info.Seq, true)
	if err != nil || !ok {
		t.Fatalf("带游标订阅失败: %v %v", ok, err)
	}
	defer sub.Close()
	if len(items) != 0 {
		t.Fatalf("afterSeq 已是当前序号，不应有补发内容: %d", len(items))
	}
	// 注册之后发布的事件必须能收到：这就是窗口期要堵住的部分。
	if err := w.Prompt(ctx, "after", "", nil); err != nil {
		t.Fatal(err)
	}
	got := drain(sub, 1)
	if len(got) == 0 {
		t.Fatal("订阅注册后发布的事件没有送达")
	}

	// 从更早的游标订阅：快照与实时流必须连续，不能有缺口。
	sub2, _, items2, ok, err := w.SubscribeWithReplay(info.Epoch, 0, true)
	if err != nil || !ok {
		t.Fatalf("补发订阅失败: %v %v", ok, err)
	}
	defer sub2.Close()
	if len(items2) == 0 {
		t.Fatal("从序号 0 订阅应拿到补发内容")
	}
	last := items2[len(items2)-1]
	if err := w.Prompt(ctx, "next", "", nil); err != nil {
		t.Fatal(err)
	}
	live := drain(sub2, 1)
	if len(live) == 0 {
		t.Fatal("补发后的实时事件没有送达")
	}
	if live[0].Seq <= last.Seq {
		t.Fatalf("实时事件序号未接续: 快照末条 %d，实时首条 %d", last.Seq, live[0].Seq)
	}
}

// Test补发与实时流无序号缺口 是 B05 的反例。
//
// 关键不变量：一次持锁内「取补发快照 + 注册订阅」时，快照末序号必须等于
// 注册时刻返回的序号。若拆成两次加锁，两次锁之间发布的事件会落在
// 「快照已结束、订阅未注册」的真空里，重连后静默丢失。
//
// 窗口只有几微秒，单轮未必命中，因此循环多轮，漏检概率随轮数指数下降。
func Test补发与实时流无序号缺口(t *testing.T) {
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

	for round := 0; round < 30; round++ {
		stop := make(chan struct{})
		started := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			// 发布量必须低于补发环与订阅队列上限；此测试只验证注册
			// 窗口期，不应把正常的超限/背压淘汰误判为事件遗漏。
			for sent := 0; sent < 16; sent++ {
				select {
				case <-stop:
					return
				default:
				}
				w.mu.Lock()
				w.publishLocked("pi.event", map[string]any{"type": "probe"})
				w.mu.Unlock()
				select {
				case started <- struct{}{}:
				default:
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
		<-started
		before := w.Info().Seq

		// 以当前序号为游标订阅：正确实现下补发为空，且返回序号应仍是 before。
		sub, info, items, ok, err := w.SubscribeWithReplay(w.Info().Epoch, before, true)
		close(stop)
		<-done
		if err != nil || !ok {
			t.Fatalf("第 %d 轮订阅失败: %v %v", round, ok, err)
		}
		// 这就是 B05 的判定点，且与是否并发无关：
		// 快照末条序号必须等于注册时刻返回的序号。若取快照与注册分成两次加锁，
		// 两次锁之间发布的事件会让 info.Seq 越过快照末序号，那些事件就此丢失。
		if len(items) > 0 {
			if last := items[len(items)-1].Seq; last != info.Seq {
				sub.Close()
				t.Fatalf("第 %d 轮快照末序号 %d 与注册序号 %d 不一致：窗口内的 %d 个事件既不在快照也不在实时流", round, last, info.Seq, info.Seq-last)
			}
		} else if info.Seq != before {
			sub.Close()
			t.Fatalf("第 %d 轮无补发内容但注册序号 %d 大于游标 %d：窗口内的事件已丢失", round, info.Seq, before)
		}
		// 顺带验证实时流从注册点之后连续。
		final := w.Info().Seq
		if final > info.Seq {
			msg, err := sub.Next(ctx)
			if err != nil {
				sub.Close()
				t.Fatalf("第 %d 轮读取实时事件失败: %v", round, err)
			}
			if msg.Seq != info.Seq+1 {
				sub.Close()
				t.Fatalf("第 %d 轮实时流序号不连续：期望 %d，实际 %d", round, info.Seq+1, msg.Seq)
			}
		}
		sub.Close()
	}
}

// Test补发失败不留下半套状态 覆盖 B05 的反向边界：
// 补发环淘汰了所需序号时必须整体失败，不能注册了订阅却让客户端以为能续上。
func Test补发失败不留下半套状态(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	before := w.SubscriberCount()
	if _, _, _, ok, err := w.SubscribeWithReplay("bogus-epoch", 0, true); ok || err != nil {
		t.Fatalf("epoch 不匹配应整体失败: ok=%v err=%v", ok, err)
	}
	if got := w.SubscriberCount(); got != before {
		t.Fatalf("失败的订阅不应占用配额: %d -> %d", before, got)
	}
	// 无游标的普通订阅不受影响。
	sub, _, _, ok, err := w.SubscribeWithReplay("", 0, false)
	if err != nil || !ok {
		t.Fatalf("普通订阅失败: %v %v", ok, err)
	}
	sub.Close()
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
