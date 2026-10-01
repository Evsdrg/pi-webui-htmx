package relay

import (
	"testing"
	"time"
)

// B53 的资源边界：单连接占用上界 = 「等待写出的字节 ≤ 预算」+「一个在写帧」。
// 小帧走独立的高优先通道，因此内容大帧排队时控制消息仍能及时送出。
func Test小帧不排在大帧后面(t *testing.T) {
	o := newOutbound()
	cancelled := false
	cancel := func() { cancelled = true }

	// 先塞满数据通道：模拟一批内容帧在排队。
	for i := 0; i < 8; i++ {
		o.send(make([]byte, controlFrameMax+1), cancel)
	}
	dataBefore := len(o.data)
	if dataBefore == 0 {
		t.Fatal("内容帧应进入数据通道")
	}
	// 控制帧（小）进另一条通道：pumpWrites 会优先排空它。
	o.send([]byte(`{"kind":"subscription_closed"}`), cancel)
	if len(o.control) != 1 {
		t.Fatalf("小帧应进入高优先通道，实际 control=%d data=%d", len(o.control), len(o.data))
	}
	if cancelled {
		t.Fatal("小帧入队不应取消连接")
	}
}

// 超预算且队列非空时，先等预算归还；只有等不到才放弃连接。
func Test超预算先等归还再放弃(t *testing.T) {
	o := newOutbound()
	cancelled := 0
	cancel := func() { cancelled++ }
	o.reserve(maxQueueBytes)

	// 这一帧会等满 enqueueTimeout 才放弃。
	start := time.Now()
	o.send(make([]byte, maxQueueBytes+1), cancel)
	waited := time.Since(start)
	if cancelled != 1 {
		t.Fatalf("等不到预算时应放弃连接，cancelled=%d", cancelled)
	}
	if waited < enqueueTimeout/2 {
		t.Fatalf("应等待预算归还而不是立刻断开，实际等待 %v", waited)
	}
}

// 记账要能回到零：反复入队 + 消费后 queued 必须归零，
// 否则连接会永久「欠费」，后续帧全被拒。
func Test记账归还后归零(t *testing.T) {
	o := newOutbound()
	cancelled := false
	cancel := func() { cancelled = true }
	for i := 0; i < 4; i++ {
		o.send(make([]byte, 1024), cancel)
	}
	if got := o.queued.Load(); got != 4*1024 {
		t.Fatalf("入队后应记 4 KiB，实际 %d", got)
	}
	for i := 0; i < 4; i++ {
		frame, ok := nextFrame(t.Context(), o)
		if !ok {
			t.Fatal("应能取出帧")
		}
		o.release(int64(len(frame)))
	}
	if got := o.queued.Load(); got != 0 {
		t.Fatalf("消费后应归零，实际 %d", got)
	}
	if cancelled {
		t.Fatal("正常入队/消费不应取消连接")
	}
}
