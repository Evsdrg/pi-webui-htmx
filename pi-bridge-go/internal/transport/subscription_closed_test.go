package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/terminal"
)

// 慢订阅者不再被摘除：只有权威帧积压到硬上限（读端彻底停止读取）才会终止
// 订阅，届时客户端收到 bridge.subscription_closed，据此重读历史与状态。
//
// 这里用假 sink 直接驱动 subscribe，让订阅在事件发布时被终止。
type fakeSink struct {
	ctx   context.Context
	mu    sync.Mutex
	msgs  []protocol.Message
	subs  map[string]*run.Subscription
	freed chan struct{}
	// slow 表示模拟慢消费者：每个事件帧的 send 停留一小会儿。
	// 停留必须**有界**——真卡死的话订阅 goroutine 永远走不到终止通知那一行，
	// 与真实情况不符（真实 send 有 outboundWait 上限）。
	slow time.Duration
}

func newFakeSink(ctx context.Context) *fakeSink {
	return &fakeSink{ctx: ctx, subs: map[string]*run.Subscription{}, freed: make(chan struct{}, 8)}
}

func (f *fakeSink) connContext() context.Context { return f.ctx }
func (f *fakeSink) send(m protocol.Message) bool {
	f.mu.Lock()
	f.msgs = append(f.msgs, m)
	f.mu.Unlock()
	if m.Kind == "control" && m.Event == "bridge.subscription_closed" {
		select {
		case f.freed <- struct{}{}:
		default:
		}
		return true
	}
	// 慢消费者：每帧停留一会儿。
	if m.Kind == "event" && f.slow > 0 {
		select {
		case <-time.After(f.slow):
		case <-f.ctx.Done():
			return false
		}
	}
	return true
}
func (f *fakeSink) trackTerminal(string, *terminal.Subscription) {}
func (f *fakeSink) dropTerminal(string)                          {}
func (f *fakeSink) trackSubscription(id string, sub *run.Subscription) {
	f.mu.Lock()
	f.subs[id] = sub
	f.mu.Unlock()
}
func (f *fakeSink) existingSubscription(id string) *run.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs[id]
}
func (f *fakeSink) dispatch(context.Context, protocol.Request) (any, error) { return nil, nil }

func (f *fakeSink) messages() []protocol.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.Message(nil), f.msgs...)
}

// Test慢消费者只丢增量不触发订阅关闭 是重设计的判定点：
// replay_burst 假 Pi 连发 120 条 text_delta（全部是可丢增量），慢消费者
// 只会丢掉其中一部分，**绝不**收到 bridge.subscription_closed。
func Test慢消费者只丢增量不触发订阅关闭(t *testing.T) {
	t.Setenv("FAKE_PI_SCRIPT", "replay_burst")
	s, manager, cwd := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	worker, err := manager.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	info := worker.Info()

	sink := newFakeSink(ctx)
	sink.slow = 2 * time.Millisecond
	params, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.subscribe(sink, protocol.Request{
		Method: "session.subscribe", SessionID: info.SessionID, Params: params,
	}); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	if err := worker.Prompt(ctx, "生成补发测试事件", "", nil); err != nil {
		t.Fatal(err)
	}
	// 等到突发结束（agent_settled 已发布）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && worker.Info().Seq < info.Seq+122 {
		time.Sleep(5 * time.Millisecond)
	}
	// 给订阅 goroutine 一点时间把事件排空。
	time.Sleep(100 * time.Millisecond)
	for _, m := range sink.messages() {
		if m.Kind == "control" && m.Event == "bridge.subscription_closed" {
			t.Fatalf("慢消费者不应被终止订阅（只丢增量），实际收到 %v", m.Data)
		}
	}
	if n := worker.SubscriberCount(); n != 1 {
		t.Fatalf("订阅应仍然注册着，实际 %d", n)
	}
}

// Test订阅被终止时控制消息带上原因 覆盖权威帧积压到硬上限的路径：
// 读端彻底停止读取时，订阅被终止并上报原因，客户端据此重读历史与状态。
func Test订阅被终止时控制消息带上原因(t *testing.T) {
	t.Setenv("FAKE_PI_SCRIPT", "authoritative_burst")
	// 把权威帧的条数硬上限压到很小：真实上限 4096 意味着读端要逐条消费
	// 四千多帧才会超限，在 -race 下要十几秒（实测 8.7 秒）——测的是同一条
	// 路径，只是不值得为它付那个时间。假 Pi 仍连发 4200 条，远超这个小上限。
	s, manager, cwd, _, _ := newTestServerWithRun(t, 2*time.Second, func(c *run.Config) {
		c.SubscriberHardLimitMessages = 8
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	worker, err := manager.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	info := worker.Info()

	sink := newFakeSink(ctx)
	params, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.subscribe(sink, protocol.Request{
		Method: "session.subscribe", SessionID: info.SessionID, Params: params,
	}); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	// 假 Pi 连发 4200 条权威帧（message_end），远超测试里设的硬上限 8；
	// 订阅 goroutine 读得慢（sink 每帧停留），队列填满后即被终止。
	sink.slow = 2 * time.Millisecond
	if err := worker.Prompt(ctx, "生成权威帧突发", "", nil); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sink.freed:
	case <-ctx.Done():
		t.Fatalf("未收到 subscription_closed: %v", ctx.Err())
	}

	var closed map[string]any
	for _, m := range sink.messages() {
		if m.Kind == "control" && m.Event == "bridge.subscription_closed" {
			closed, _ = m.Data.(map[string]any)
		}
	}
	if closed == nil {
		t.Fatal("没有发出 subscription_closed")
	}
	if closed["resyncRequired"] != true {
		t.Fatalf("必须保留 resyncRequired=true，实际 %v", closed["resyncRequired"])
	}
	reason, _ := closed["reason"].(string)
	if reason != run.DropReasonQueueFull {
		t.Fatalf("reason 应为 %q，实际 %q", run.DropReasonQueueFull, reason)
	}
}

// Test正常关闭订阅不触发终止通知 是「原因=""」那批 WARN 的反例。
//
// 订阅有两种结束方式：被背压终止（要做重读），和正常关闭（退订、换会话、
// 重订替换旧订阅、连接断开）。两者在 Subscription 上都是「通道关闭」，
// 但只有前者需要客户端重读历史。此前传输层不区分二者，于是每次刷新页面、
// 切换会话、重连重订都会给客户端发一条 resyncRequired 的控制帧——正是
// 「实时订阅已结束，正在核对任务状态」这句提示的来源之一。
func Test正常关闭订阅不触发终止通知(t *testing.T) {
	s, manager, cwd := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	worker, err := manager.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	info := worker.Info()

	sink := newFakeSink(ctx)
	params, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.subscribe(sink, protocol.Request{
		Method: "session.subscribe", SessionID: info.SessionID, Params: params,
	}); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	// 正常关闭：等价于客户端退订，或同一连接重订时替换掉旧订阅。
	sub := sink.existingSubscription(info.SessionID)
	if sub == nil {
		t.Fatal("订阅未登记")
	}
	sub.Close()
	// 给订阅 goroutine 留出走到通道关闭那一行的时间。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range sink.messages() {
			if m.Kind == "control" && m.Event == "bridge.subscription_closed" {
				t.Fatalf("正常关闭不得发终止通知（客户端会误以为要重读）: %v", m.Data)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
