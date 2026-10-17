package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/terminal"
)

// 慢订阅者被摘除时，客户端只会看到「实时订阅已结束」并据此重读历史。
// 若不给出原因，事后无从分辨是队列满还是字节上限——两者的处置完全不同。
// 这里用假 sink 直接驱动 subscribeWithReplay，让订阅在事件发布时被摘除。
type fakeSink struct {
	ctx   context.Context
	mu    sync.Mutex
	msgs  []protocol.Message
	subs  map[string]*runtime.Subscription
	freed chan struct{}
	// slow 表示模拟慢消费者：每个事件帧的 send 停留一小会儿。
	// 停留必须**有界**——真卡死的话订阅 goroutine 永远走不到摘除通知那一行，
	// 与真实情况不符（真实 send 有 outboundWait 上限）。
	slow time.Duration
}

func newFakeSink(ctx context.Context) *fakeSink {
	return &fakeSink{ctx: ctx, subs: map[string]*runtime.Subscription{}, freed: make(chan struct{}, 8)}
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
	// 慢消费者：每帧停留一会儿。120 条突发 × 2ms 远超过 32 条队列容量，
	// 队列溢出后 publishLocked 摘除订阅，goroutine 读到关闭的通道即上报。
	if m.Kind == "event" && f.slow > 0 {
		select {
		case <-time.After(f.slow):
		case <-f.ctx.Done():
			return false
		}
	}
	return true
}
func (f *fakeSink) sendRaw(context.Context, []byte) bool         { return true }
func (f *fakeSink) trackTerminal(string, *terminal.Subscription) {}
func (f *fakeSink) dropTerminal(string)                          {}
func (f *fakeSink) trackSubscription(id string, sub *runtime.Subscription) {
	f.mu.Lock()
	f.subs[id] = sub
	f.mu.Unlock()
}
func (f *fakeSink) existingSubscription(id string) *runtime.Subscription {
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

func Test订阅被摘除时控制消息带上原因(t *testing.T) {
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
	// 每帧 2ms：120 条突发下消费速率远低于发布速率，32 条队列必然溢出。
	sink.slow = 2 * time.Millisecond
	params, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.subscribeWithReplay(sink, protocol.Request{
		Method: "session.subscribe", SessionID: info.SessionID, Params: params,
	}); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	// 订阅建立后立刻开始突发。事件的消费端是订阅 goroutine 自己（Next → send），
	// 所以「慢」不体现在 sink 上，而体现在 worker 的队列容量：publishLocked 在
	// 32 条队列填满时摘除。sink 只需让出一个调度窗口，队列就会溢出。
	if err := worker.Prompt(ctx, "生成补发测试事件", "", nil); err != nil {
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
	if reason != runtime.DropReasonQueueFull && reason != runtime.DropReasonByteLimit {
		t.Fatalf("reason 应为具体摘除原因，实际 %q", reason)
	}
}
