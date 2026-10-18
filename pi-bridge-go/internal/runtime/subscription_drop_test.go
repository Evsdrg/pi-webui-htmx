package runtime

import (
	"testing"
)

// 慢订阅者的处置在 2026-10-18 重设计后分成两类：
//
//   - 可丢弃的增量帧：条数或字节超过**软上限**时丢弃，订阅存活。客户端
//     只会少几帧流式片段，回合结束时由权威帧补齐终态。
//   - 不可丢弃的权威帧：条数或字节超过**硬上限**才终止订阅。那种情况说明
//     读端已彻底停止读取，继续留在内存里只会无界增长。
//
// 终止时仍记录原因，供诊断分辨是哪条限额触发的。
func Test慢订阅者的处置按事件类型区分(t *testing.T) {
	newWorker := func(messages, bytes, hardLimit int) (*Worker, *Subscription) {
		cfg := Defaults()
		cfg.SubscriberMessages = messages
		cfg.SubscriberBytes = bytes
		cfg.SubscriberHardLimitMessages = hardLimit
		w := &Worker{cfg: cfg, id: "s", epoch: "e"}
		w.subs = map[*Subscription]struct{}{}
		sub := &Subscription{worker: w, ch: make(chan queuedEvent, hardLimit)}
		w.subs[sub] = struct{}{}
		return w, sub
	}

	t.Run("增量帧超软条数上限时丢弃而不终止", func(t *testing.T) {
		w, sub := newWorker(1, 1<<20, 4096)
		delta := map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "x"}}
		w.publishLocked("pi.event", delta)
		w.publishLocked("pi.event", delta)
		if got := sub.DropReason(); got != "" {
			t.Fatalf("增量溢出不应终止订阅，实际原因 %q", got)
		}
		if n := w.SubscriberCount(); n != 1 {
			t.Fatalf("订阅应仍然注册着，实际 %d", n)
		}
		sub.Close()
	})

	t.Run("增量帧超字节预算时丢弃而不终止", func(t *testing.T) {
		w, sub := newWorker(64, 100, 4096)
		big := map[string]any{"type": "tool_execution_update", "toolCallId": "c1", "partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(make([]byte, 200))}}}}
		w.publishLocked("pi.event", big)
		if got := sub.DropReason(); got != "" {
			t.Fatalf("超字节预算的增量应被丢弃而不是终止，实际 %q", got)
		}
		sub.Close()
	})

	t.Run("权威帧积压超过硬上限才终止", func(t *testing.T) {
		// 硬上限 2：第一条入队，第二条填满，第三条超限即终止。
		w, sub := newWorker(1, 1<<20, 2)
		w.publishLocked("pi.event", map[string]any{"type": "message_end"})
		w.publishLocked("pi.event", map[string]any{"type": "message_end"})
		w.publishLocked("pi.event", map[string]any{"type": "message_end"})
		if got := sub.DropReason(); got != DropReasonQueueFull {
			t.Fatalf("权威帧积压应记录 %q，实际 %q", DropReasonQueueFull, got)
		}
	})

	t.Run("正常订阅没有终止原因", func(t *testing.T) {
		w, sub := newWorker(8, 1<<20, 4096)
		w.publishLocked("pi.event", map[string]any{"type": "agent_start"})
		if got := sub.DropReason(); got != "" {
			t.Fatalf("未被终止时原因应为空，实际 %q", got)
		}
		sub.Close()
	})

	// 权威帧的字节硬上限单独生效：条数远未到上限，但一条大事件即可终止。
	t.Run("权威帧超字节硬上限也终止", func(t *testing.T) {
		cfg := Defaults()
		cfg.SubscriberMessages = 8
		cfg.SubscriberHardLimitMessages = 4096
		cfg.SubscriberHardLimitBytes = 64
		w := &Worker{cfg: cfg, id: "s", epoch: "e"}
		w.subs = map[*Subscription]struct{}{}
		sub := &Subscription{worker: w, ch: make(chan queuedEvent, cfg.SubscriberHardLimitMessages)}
		w.subs[sub] = struct{}{}
		w.publishLocked("pi.event", map[string]any{"type": "message_end", "message": string(make([]byte, 200))})
		if got := sub.DropReason(); got != DropReasonQueueFull {
			t.Fatalf("超字节硬上限应终止订阅，实际原因 %q", got)
		}
	})
}
