package runtime

import (
	"context"
	"testing"
	"time"
)

// 慢订阅者被摘除是设计行为（绝不阻塞 Pi 输出），但客户端只会看到
// 「实时订阅已结束」。若没有原因，事后无从分辨是队列满还是字节上限触发——
// 两者的处置完全不同：前者要降渲染频率，后者要处理单条大事件。
//
// 这里直接构造 Subscription 注册进 worker，绕开 Subscribe 对进程信息的依赖：
// 被测的是 publishLocked 的摘除路径与 DropReason，不是订阅创建。
func Test慢订阅者被摘除时记录原因(t *testing.T) {
	newWorker := func(messages, bytes int) (*Worker, *Subscription) {
		cfg := Defaults()
		cfg.SubscriberMessages = messages
		cfg.SubscriberBytes = bytes
		w := &Worker{cfg: cfg, id: "s", epoch: "e"}
		w.replay = newReplayRing(cfg)
		w.subs = map[*Subscription]struct{}{}
		sub := &Subscription{worker: w, ch: make(chan queuedEvent, messages)}
		w.subs[sub] = struct{}{}
		return w, sub
	}

	t.Run("队列满", func(t *testing.T) {
		w, sub := newWorker(1, 1<<20)
		// 第一条填满队列（容量 1），第二条没有空位即摘除。
		w.publishLocked("pi.event", map[string]string{"x": "1"})
		w.publishLocked("pi.event", map[string]string{"x": "2"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := sub.Next(ctx); err != nil {
			t.Fatalf("应先读到已入队的事件: %v", err)
		}
		if _, err := sub.Next(ctx); err == nil {
			t.Fatal("通道已关闭，第二次读取必须失败")
		}
		if got := sub.DropReason(); got != DropReasonQueueFull {
			t.Fatalf("摘除原因应为 %q，实际 %q", DropReasonQueueFull, got)
		}
	})

	t.Run("字节上限", func(t *testing.T) {
		w, sub := newWorker(64, 100)
		// 单条事件超过字节预算：即使队列还空着也必须摘除。
		w.publishLocked("pi.event", map[string]string{"x": "0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := sub.Next(ctx); err == nil {
			t.Fatal("超字节预算的订阅必须被摘除")
		}
		if got := sub.DropReason(); got != DropReasonByteLimit {
			t.Fatalf("摘除原因应为 %q，实际 %q", DropReasonByteLimit, got)
		}
	})

	t.Run("正常订阅没有摘除原因", func(t *testing.T) {
		w, sub := newWorker(8, 1<<20)
		w.publishLocked("pi.event", map[string]string{"x": "1"})
		if got := sub.DropReason(); got != "" {
			t.Fatalf("未被摘除时原因应为空，实际 %q", got)
		}
		sub.Close()
	})
}
