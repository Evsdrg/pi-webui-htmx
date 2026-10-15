package transport

import (
	"context"
	"testing"

	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
)

// busySessions 只收 busy 为真的会话：空闲但存活的工作进程不该让侧栏转圈
// （它会闲置到被回收）。这是「看不到会话活动」修复的取数口径。
func TestBusySessions只取忙碌会话(t *testing.T) {
	got := busySessions([]run.Info{
		{SessionID: "a", Busy: true},
		{SessionID: "b", Busy: false},
		{SessionID: "c", Busy: true},
	})
	if len(got) != 2 || !got["a"] || !got["c"] {
		t.Fatalf("应只含忙碌会话 a、c，实际 %v", got)
	}
	if got["b"] {
		t.Fatalf("空闲会话不该入选：%v", got)
	}
}

// diffBusy 是「只在变化时广播」的判据：相同快照必须判为无变化，
// 否则每 2 秒都会给所有浏览器推一帧无意义的广播。
func TestDiffBusy变化检测(t *testing.T) {
	base := map[string]bool{"a": true}
	cases := []struct {
		name    string
		prev    map[string]bool
		next    map[string]bool
		changed bool
	}{
		{"完全相同", map[string]bool{"a": true}, map[string]bool{"a": true}, false},
		{"都为空", map[string]bool{}, map[string]bool{}, false},
		{"新增一个", map[string]bool{"a": true}, map[string]bool{"a": true, "b": true}, true},
		{"减少一个", map[string]bool{"a": true, "b": true}, map[string]bool{"a": true}, true},
		{"从空到有", map[string]bool{}, map[string]bool{"a": true}, true},
		{"从有到空", map[string]bool{"a": true}, map[string]bool{}, true},
		{"换了一个但数量相同", map[string]bool{"a": true}, map[string]bool{"b": true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := diffBusy(c.prev, c.next); got != c.changed {
				t.Fatalf("diffBusy(%v, %v) = %v，期望 %v", c.prev, c.next, got, c.changed)
			}
		})
	}
	if diffBusy(nil, base) != true {
		t.Fatal("从 nil 到非空应判为变化")
	}
}

// trySend 是忙闲广播的投递方式：这是可重放的冗余状态，队列满时必须
// **丢弃这一帧**，绝不能等待或断开连接——否则一条慢速浏览器会仅仅因为
// 侧栏在刷新而被踢掉，代价与收益完全不成比例。
func TestTrySend队列满时丢弃且不断开连接(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 深度 1 的帧通道：第二次 trySend 必定无处可去。
	out := make(chan []byte, 1)
	queue := &outboundQueue{frames: out, space: make(chan struct{})}
	c := &connection{ctx: ctx, cancel: cancel, queue: queue}

	message := protocol.Message{Version: 1, Kind: "control", Event: "bridge.workers_changed", Data: map[string]any{"running": []string{"a"}}}
	c.trySend(message) // 填满帧通道
	used := queue.bytes()
	if used == 0 {
		t.Fatal("第一帧应当占用预算")
	}
	c.trySend(message) // 应被丢弃
	c.trySend(message) // 仍应被丢弃

	if ctx.Err() != nil {
		t.Fatal("队列满时 trySend 不得取消连接")
	}
	// 被丢弃的帧既不能泄漏预算（没回滚），也不能回收在队帧的预算（误回滚）。
	if queue.bytes() != used {
		t.Fatalf("丢弃不该改变占用预算：%d → %d", used, queue.bytes())
	}
	if len(out) != 1 {
		t.Fatalf("只应有一帧入队，实际 %d", len(out))
	}
}

// 预算不足时同样只丢弃，不等待（tryReserve 不阻塞）。
func TestTrySend预算不足时立即丢弃(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan []byte, 8)
	queue := &outboundQueue{frames: out, space: make(chan struct{})}
	c := &connection{ctx: ctx, cancel: cancel, queue: queue}
	// 先把字节预算占满。
	queue.tryReserve(outboundQueueLimit)
	c.trySend(protocol.Message{Version: 1, Kind: "control", Event: "bridge.workers_changed"})
	if len(out) != 0 {
		t.Fatalf("预算不足时不该入队，实际 %d 帧", len(out))
	}
	if ctx.Err() != nil {
		t.Fatal("预算不足时 trySend 不得取消连接")
	}
}
