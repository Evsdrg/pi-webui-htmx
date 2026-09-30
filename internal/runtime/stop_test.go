package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

// Test停止流程超时后再次Stop不阻塞 覆盖 B50：
// 第一次停止（关闭 stdin → SIGTERM → SIGKILL）每段都有宽限期，强杀超时后
// 进程仍可能不退出。那时工人处于 closing 状态但 done 永不关闭，旧实现里
// 第二次 Stop 会无条件 `<-w.done`，把调用者永久挂住——而它是要回复
// session.stop 的请求协程，于是那条命令的槽位与请求号一起泄漏。
//
// 「进程杀不掉」没法在测试里真造出来（SIGKILL 不可忽略），所以这里直接
// 构造那个状态：closing 已置位、done 未关闭、第一次流程的预算已耗尽。
func Test停止流程超时后再次Stop不阻塞(t *testing.T) {
	w := &Worker{
		cfg: Config{StopGrace: 50 * time.Millisecond},
		// 进程还在：done 没有任何人会关。
		done: make(chan struct{}),
		// 第一次流程已经开始，且它的预算已经用完。
		closing:      true,
		stopDeadline: time.Now().Add(-time.Second),
	}

	done := make(chan error, 1)
	go func() { done <- w.Stop(false) }()
	select {
	case err := <-done:
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.Code != "timeout" {
			t.Fatalf("应当明确回报停止超时，实际 %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("第二次 Stop 阻塞：closing 之后的等待没有上限")
	}
}

// Test停止进行中再次Stop等到进程退出 是上一条的另一半：
// 已有停止流程在跑时，第二次调用应当等到进程真的退出再返回成功，
// 而不是超时或直接谎报成功。
func Test停止进行中再次Stop等到进程退出(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟「第一次停止正在进行」：它已经置了 closing，预算还没用完。
	w.mu.Lock()
	w.closing = true
	w.stopDeadline = time.Now().Add(5 * time.Second)
	w.mu.Unlock()

	// 真正让进程退出：fake-pi 读到 stdin 关闭就返回。
	go func() {
		time.Sleep(80 * time.Millisecond)
		w.client.CloseInput()
	}()

	start := time.Now()
	if err := w.Stop(false); err != nil {
		t.Fatalf("进程退出后第二次 Stop 应当成功: %v", err)
	}
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Fatalf("没有等到进程退出就返回了（只用了 %v）", waited)
	}
}
