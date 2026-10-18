package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

// 慢消费者不再被摘除，只丢可重建的增量。
//
// 旧设计：订阅积压超过条数或字节上限就整条摘掉，客户端只能看到
// 「实时订阅已结束」并靠轮询猜状态——而权威事件（agent_settled）正好
// 随订阅一起消失，界面就停在「运行中」。
//
// 新设计：publish 区分两类事件。增量帧（message_update 的 *_delta、
// tool_execution_update）丢了会被后续帧覆盖，可以直接丢弃；权威帧
// （agent_start/end、agent_settled、message_end、tool_execution_end 等）
// 在硬上限内必投，超了才终止订阅——那种情况说明读端已彻底停止读取。
func Test慢消费者只丢增量不被摘除(t *testing.T) {
	newWorker := func(messages, hardLimit int) (*Worker, *Subscription) {
		cfg := Defaults()
		cfg.SubscriberMessages = messages
		cfg.SubscriberHardLimitMessages = hardLimit
		w := &Worker{cfg: cfg, id: "s", epoch: "e"}
		w.subs = map[*Subscription]struct{}{}
		sub := &Subscription{worker: w, ch: make(chan queuedEvent, hardLimit)}
		w.subs[sub] = struct{}{}
		return w, sub
	}

	t.Run("增量帧溢出时丢弃，订阅仍然活着", func(t *testing.T) {
		// 软上限 1：第一条增量入队，第二条因超软上限被丢。
		w, sub := newWorker(1, 4096)
		w.publishLocked("pi.event", map[string]any{
			"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "a"},
		})
		w.publishLocked("pi.event", map[string]any{
			"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "b"},
		})
		if got := sub.DropReason(); got != "" {
			t.Fatalf("增量溢出不应终止订阅，实际原因 %q", got)
		}
		if n := w.SubscriberCount(); n != 1 {
			t.Fatalf("订阅应仍然注册着，实际 %d", n)
		}
		if got := sub.dropped.Load(); got != 1 {
			t.Fatalf("应记录一次丢弃，实际 %d", got)
		}
		// 权威帧必须仍然送得出去（客户端要靠它结束回合）。
		{
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = sub.Next(ctx) // 先取走占位的那条增量
			cancel()
		}
		w.publishLocked("pi.event", map[string]any{"type": "agent_settled"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		msg, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("权威帧必须送达: %v", err)
		}
		if got := eventType(msg); got != "agent_settled" {
			t.Fatalf("应收到 agent_settled，实际 %q", got)
		}
		sub.Close()
	})

	t.Run("tool_execution_update 同样可丢", func(t *testing.T) {
		w, sub := newWorker(1, 4096)
		w.publishLocked("pi.event", map[string]any{"type": "tool_execution_update", "toolCallId": "c1"})
		w.publishLocked("pi.event", map[string]any{"type": "tool_execution_update", "toolCallId": "c1"})
		if got := sub.DropReason(); got != "" {
			t.Fatalf("工具增量溢出不应终止订阅，实际 %q", got)
		}
		sub.Close()
	})

	t.Run("权威帧积压到硬上限才终止流", func(t *testing.T) {
		// 硬上限 4：连发 5 条权威帧（无人读取）会填满队列并超限。
		cfg := Defaults()
		cfg.SubscriberMessages = 2
		cfg.SubscriberHardLimitMessages = 4
		w := &Worker{cfg: cfg, id: "s", epoch: "e"}
		w.subs = map[*Subscription]struct{}{}
		sub := &Subscription{worker: w, ch: make(chan queuedEvent, cfg.SubscriberHardLimitMessages)}
		w.subs[sub] = struct{}{}
		for i := 0; i < 5; i++ {
			w.publishLocked("pi.event", map[string]any{"type": "message_end"})
		}
		if got := sub.DropReason(); got != DropReasonQueueFull {
			t.Fatalf("权威帧积压超过硬上限应终止订阅，实际原因 %q", got)
		}
		if n := w.SubscriberCount(); n != 0 {
			t.Fatalf("终止后不应仍注册着，实际 %d", n)
		}
	})
}

// eventType 取出事件消息里 data.type（Pi 事件的类型名）。
func eventType(msg protocol.Message) string {
	data, ok := msg.Data.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := data["type"].(string)
	return s
}

// Test可丢帧判定认两种入参形态 是这次重设计里最容易漏的一条：
// 生产路径上 Pi 事件是 json.RawMessage（event() 未解析就转发），桥自己发的
// 事件才是 map[string]any。若判定只认 map，生产路径会把**所有**事件当成
// 权威帧——增量照样积压到硬上限、订阅照样被终止，等于没修。
func Test可丢帧判定认两种入参形态(t *testing.T) {
	raw := json.RawMessage(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"x"}}`)
	if !droppableEvent(raw) {
		t.Fatal("json.RawMessage 形态的 text_delta 应判为可丢")
	}
	asMap := map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "x"}}
	if !droppableEvent(asMap) {
		t.Fatal("map 形态的 text_delta 应判为可丢")
	}
	// 权威帧在两种形态下都不可丢。
	if droppableEvent(json.RawMessage(`{"type":"agent_settled"}`)) {
		t.Fatal("agent_settled 不可丢")
	}
	if droppableEvent(map[string]any{"type": "agent_settled"}) {
		t.Fatal("agent_settled 不可丢")
	}
	if droppableEvent(json.RawMessage(`{"type":"message_update","assistantMessageEvent":{"type":"text_end"}}`)) {
		t.Fatal("非 _delta 的 message_update 不可丢")
	}
}
