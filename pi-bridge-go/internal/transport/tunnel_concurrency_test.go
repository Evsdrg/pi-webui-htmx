package transport

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"pi-bridge-go/internal/testutil"
)

// Test隧道并发终端命令保护terms映射 覆盖 B73：
// virtualConn.terms 曾被 trackTerminal/dropTerminal/releaseAll 无锁访问，
// 而 handle 只把 session.subscribe/unsubscribe 串行化（B73 当初只看到 subs），
// terminal.* 仍走并行分发。并发开终端在 -race 下会报 DATA RACE。
// 本用例同时交错 releaseAll（连接死亡时走的那条路），覆盖两侧竞争。
func Test隧道并发终端命令保护terms映射(t *testing.T) {
	s, _, cwd := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, frame)
		return nil
	}, 8, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	boot := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "boot",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-terms", boot)) {
		t.Fatal("隧道帧未被处理")
	}
	waitFrames(t, &mu, &sent, 1)

	bridge.mu.Lock()
	conn := bridge.virtual["tab-terms"]
	bridge.mu.Unlock()
	if conn == nil {
		t.Fatal("虚拟连接未建立")
	}

	// 一边并发登记终端，一边清空——两侧都要写 c.terms。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			open := mustFrame(t, map[string]any{
				"version": 1, "kind": "command", "requestId": fmt.Sprintf("t%d", i),
				"method": "terminal.open", "params": map[string]any{"cwd": cwd, "cols": 80, "rows": 24},
			})
			bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-terms", open))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 8; j++ {
			conn.releaseAll()
		}
	}()
	wg.Wait()
	waitFrames(t, &mu, &sent, 5)

	// 收尾再单独开一个终端，确认它真的登记进了 terms——保证上面的并发
	// 确实命中了写入路径，而不是全被 busy 拒掉（否则用例退化成空跑）。
	last := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "last",
		"method": "terminal.open", "params": map[string]any{"cwd": cwd, "cols": 80, "rows": 24},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-terms", last)) {
		t.Fatal("收尾的终端开启未被处理")
	}
	testutil.WaitFor(t, "并发终端命令登记进 terms", func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		return len(conn.terms) > 0
	})

	// 清空后不得残留句柄。
	conn.releaseAll()
	bridge.mu.Lock()
	left := len(conn.terms)
	bridge.mu.Unlock()
	if left != 0 {
		t.Fatalf("releaseAll 后仍残留 %d 个终端句柄", left)
	}
}

// Test隧道死连接重连不死锁 覆盖 B73 同期发现的第二处：
// acquire 在持有 t.mu（defer 解锁）时调用 releaseAll，而 releaseAll 又要拿同一把
// 不可重入的 mutex。命中前提是「pump 已把 dead 置位、但还没从映射里摘除」这个窗口，
// 正常时序下几乎撞不到——所以这里手工构造该状态，用超时把死锁变成可判定的失败。
func Test隧道死连接重连不死锁(t *testing.T) {
	s, _, cwd := newTestServer(t)
	bridge := NewTunnelBridge(s, func(frame []byte) error { return nil }, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	first := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "d1",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-dead", first)) {
		t.Fatal("隧道帧未被处理")
	}
	// 构造「dead 已置位、但仍在映射里」的状态：markDead 的摘除与置位之间。
	bridge.mu.Lock()
	conn := bridge.virtual["tab-dead"]
	bridge.mu.Unlock()
	if conn == nil {
		t.Fatal("虚拟连接未建立")
	}
	conn.dead.Store(true)

	second := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "d2", "method": "worker.list",
	})
	done := make(chan bool, 1)
	go func() {
		done <- bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-dead", second))
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("死连接重连未被处理")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("同 clientId 重连卡死：acquire 持锁调用了会再次加锁的 releaseAll")
	}
}
