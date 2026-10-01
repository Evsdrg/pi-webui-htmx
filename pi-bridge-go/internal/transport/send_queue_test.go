package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

// 归还时广播：一次释放够多个等待者用时，全部都要被叫醒。
//
// 这条钉住的是「通知」而不是「轮询」。单播式唤醒（只叫一个等待者）会让
// 其余的人一直睡到下一次归还——而如果消费者此时恰好没有别的东西可发，
// 它们就要白等到超时。旧实现靠 2ms 轮询绕过了这个问题；换成通知之后
// 广播必须是真的广播。
func Test一次归还唤醒全部等待者(t *testing.T) {
	q := newOutboundQueue(64)
	// 两帧正好把 1MiB 预算填满：单帧上限与队列预算都是 512KiB 的整数倍，
	// 这里用满额而不是「差不多满」，否则剩下的零头会让等待者直接通过。
	for i := 0; i < 2; i++ {
		if !q.enqueue(context.Background(), make([]byte, outboundFrameLimit)) {
			t.Fatal("填满预算失败")
		}
	}
	if got := q.bytes(); got != outboundQueueLimit {
		t.Fatalf("预算应正好占满：%d / %d", got, outboundQueueLimit)
	}
	// 三个等待者都会被预算挡住，分别睡在信号通道上。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 各等各的 ctx：这里刻意不给它们超时，只靠归还唤醒。
			results <- q.enqueue(ctx, make([]byte, 20<<10))
		}()
	}
	select {
	case <-results:
		t.Fatal("预算已满时不应有等待者提前返回")
	case <-time.After(50 * time.Millisecond):
	}
	// 消费者取走一帧：这一步归还 512KiB，三个人各要 20KiB，够全部通过。
	first := <-q.frames
	q.release(int64(len(first)))

	deadline := time.After(2 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case ok := <-results:
			if !ok {
				t.Fatal("归还后等待者仍被拒绝")
			}
		case <-deadline:
			t.Fatalf("只叫醒了 %d 个等待者：广播没有生效", i)
		}
	}
	wg.Wait()
	// 512KiB（剩余的一帧）+ 3×20KiB 的预留。
	if got, want := q.bytes(), int64(outboundFrameLimit+3*(20<<10)); got != want {
		t.Fatalf("预算记账错误：得到 %d，期望 %d", got, want)
	}
}

// 没人在等时归还不分配、也不丢信息：之后到来的等待者直接拿到预算。
func Test无人等待时的归还不丢空间(t *testing.T) {
	q := newOutboundQueue(8)
	frame := make([]byte, outboundFrameLimit)
	if !q.enqueue(context.Background(), frame) {
		t.Fatal("首帧入队失败")
	}
	first := <-q.frames
	q.release(int64(len(first)))
	if got := q.bytes(); got != 0 {
		t.Fatalf("归还后预算应为 0，得到 %d", got)
	}
	// 现在预约同样的量必须立刻成功（不靠轮询也不会阻塞）。
	done := make(chan bool, 1)
	go func() { done <- q.reserve(context.Background(), outboundFrameLimit) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("归还后的空间应当可用")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("归还后的预约不应等待")
	}
	q.release(outboundFrameLimit)
}

// 等待被取消时不得留下「以为有人等」的计数，否则后续归还会白白换代。
func Test等待取消后不再占用唤醒计数(t *testing.T) {
	q := newOutboundQueue(8)
	// 填满预算，这样下面那个等待者只能靠唤醒离开。
	for i := 0; i < 2; i++ {
		if !q.enqueue(context.Background(), make([]byte, outboundFrameLimit)) {
			t.Fatal("填满预算失败")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- q.reserve(ctx, 1) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("取消后不应拿到预算")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消没有唤醒等待者")
	}
	q.mu.Lock()
	waiting := q.waiting
	q.mu.Unlock()
	if waiting != 0 {
		t.Fatalf("取消后等待计数应为 0，得到 %d", waiting)
	}
	q.release(outboundFrameLimit)
}

// 超预算的帧直接拒绝，不进入等待（否则会永远等不到）。
func Test超出单帧上限直接拒绝(t *testing.T) {
	q := newOutboundQueue(8)
	if q.enqueue(context.Background(), make([]byte, outboundFrameLimit+1)) {
		t.Fatal("超过单帧上限的帧不应入队")
	}
	if q.enqueue(context.Background(), nil) {
		t.Fatal("空帧不应入队")
	}
	if got := q.bytes(); got != 0 {
		t.Fatalf("被拒绝的帧不应占用预算：%d", got)
	}
}

// Test预算等待失败时不报告入队成功：reserve 因超时或取消拿不到预算时，
// enqueue 必须返回 false。返回 true 而帧没进队列，调用方会当作投递成功，
// 那一帧（可能是回执或事件推送）就凭空消失了。
func Test预算等待失败时不报告入队成功(t *testing.T) {
	q := newOutboundQueue(8)
	for i := 0; i < 2; i++ {
		if !q.enqueue(context.Background(), make([]byte, outboundFrameLimit)) {
			t.Fatal("填满预算失败")
		}
	}
	// 预算已被占满，这次投递只能等到超时。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if q.enqueue(ctx, make([]byte, 1<<10)) {
		t.Fatal("预算等待超时后不得报告入队成功")
	}
	if got := q.bytes(); got != outboundQueueLimit {
		t.Fatalf("被拒绝的帧不应改变占用：%d", got)
	}

	// 取消与超时走同一条路径，结果必须一致。
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if q.enqueue(ctx2, make([]byte, 1<<10)) {
		t.Fatal("预算等待被取消后不得报告入队成功")
	}
}
