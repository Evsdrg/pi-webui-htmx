package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTunnelBridge命令闭环(t *testing.T) {
	s, _, cwd := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, frame)
		return nil
	}, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	// 通过隧道启动会话。
	frame := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "t1",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", frame)) {
		t.Fatal("隧道帧未被处理")
	}
	waitFrames(t, &mu, &sent, 1)
	reply := decodeFrame(t, lastFrame(t, &mu, &sent))
	if reply["requestId"] != "t1" || reply["ok"] != true {
		t.Fatalf("隧道响应异常: %v", reply)
	}
	data, _ := reply["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("隧道启动未返回 sessionId: %v", reply)
	}

	// 同一 clientId 的后续命令复用同一虚拟连接。
	frame = mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "t2", "sessionId": sessionID,
		"method": "session.state",
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", frame)) {
		t.Fatal("第二条隧道帧未被处理")
	}
	waitFrames(t, &mu, &sent, 2)
	reply = decodeFrame(t, lastFrame(t, &mu, &sent))
	if reply["requestId"] != "t2" || reply["ok"] != true {
		t.Fatalf("隧道状态查询异常: %v", reply)
	}
	if stats := bridge.Stats(); stats["virtualConnections"].(int) != 1 {
		t.Fatalf("同一 clientId 应复用虚拟连接: %v", stats)
	}
}

func TestTunnelBridge重复requestId不重复执行(t *testing.T) {
	s, _, cwd := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, frame)
		return nil
	}, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)
	frame := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "dup-t",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", frame))
	waitFrames(t, &mu, &sent, 1)
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", frame))
	waitFrames(t, &mu, &sent, 2)
	reply := decodeFrame(t, lastFrame(t, &mu, &sent))
	data, _ := reply["data"].(map[string]any)
	if data == nil || data["duplicate"] != true {
		t.Fatalf("重复 requestId 应回放结论: %v", reply)
	}
	if len(s.manager.List()) != 1 {
		t.Fatalf("不得重复启动工作进程: %+v", s.manager.List())
	}
}

func TestTunnelBridge拒绝非法帧(t *testing.T) {
	s, _, _ := newTestServer(t)
	bridge := NewTunnelBridge(s, nil, 4, time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)
	for _, bad := range []string{"", "not json", `{"to":"x"}`} {
		if bridge.HandleFrame(context.Background(), []byte(bad)) {
			t.Fatalf("非法帧 %q 不应被处理", bad)
		}
	}
}

func TestTunnelBridge虚拟连接上限(t *testing.T) {
	s, _, cwd := newTestServer(t)
	bridge := NewTunnelBridge(s, func(frame []byte) error { return nil }, 2, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)
	for i := 0; i < 5; i++ {
		frame := mustFrame(t, map[string]any{
			"version": 1, "kind": "command", "requestId": "r" + string(rune('a'+i)),
			"method": "session.start", "params": map[string]any{"cwd": cwd},
		})
		bridge.HandleFrame(context.Background(), wrapFrom(t, string(rune('a'+i)), frame))
	}
	if stats := bridge.Stats(); stats["virtualConnections"].(int) != 2 {
		t.Fatalf("虚拟连接数应受上限约束: %v", stats)
	}
}

func TestTunnelBridge空闲回收(t *testing.T) {
	s, _, cwd := newTestServer(t)
	bridge := NewTunnelBridge(s, func(frame []byte) error { return nil }, 4, 100*time.Millisecond)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)
	frame := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "idle-1",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-idle", frame))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if bridge.Stats()["virtualConnections"].(int) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("空闲虚拟连接未被回收: %v", bridge.Stats())
}

// wrapFrom 构造 relay 到桥方向的路由帧。
func wrapFrom(t *testing.T, clientID string, payload []byte) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"from": clientID, "data": json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustFrame(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeFrame(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(frame, &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", frame)
	}
	return m
}

// lastFrame 取最后一帧并解出隧道路由封装内的业务载荷。
func lastFrame(t *testing.T, mu *sync.Mutex, sent *[][]byte) []byte {
	t.Helper()
	mu.Lock()
	list := *sent
	mu.Unlock()
	if len(list) == 0 {
		return nil
	}
	raw := list[len(list)-1]
	var envelope struct {
		To   string          `json:"to"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 {
		t.Fatalf("隧道回帧缺少路由封装: %s", raw)
	}
	if envelope.To != "tab-1" {
		t.Fatalf("回帧目标浏览器不正确: %s", raw)
	}
	return envelope.Data
}

func waitFrames(t *testing.T, mu *sync.Mutex, sent *[][]byte, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*sent)
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 帧超时", want)
}

// Test隧道发送失败后不再复用死连接 覆盖 B57：
// pump 因发送失败退出后，连接以前仍留在映射里，同一 clientId 重连
// 会复用它，响应入队却无人发送。
func Test隧道发送失败后不再复用死连接(t *testing.T) {
	s, _, cwd := newTestServer(t)
	fail := atomic.Bool{}
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		if fail.Load() {
			return errors.New("隧道已断开")
		}
		return nil
	}, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	first := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "d1",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", first)) {
		t.Fatal("隧道帧未被处理")
	}
	// 让隧道进入失败状态，迫使 pump 退出。
	fail.Store(true)
	waitFor(t, func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		_, ok := bridge.virtual["tab-1"]
		return !ok
	})

	// 同一 clientId 再次来访：必须拿到新连接，响应能真的发出去。
	fail.Store(false)
	second := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "d2", "method": "worker.list",
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", second)) {
		t.Fatal("重连后的隧道帧未被处理")
	}
	waitFor(t, func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		c, ok := bridge.virtual["tab-1"]
		return ok && !c.dead.Load()
	})
}

// Test隧道退订释放订阅配额 覆盖 B14：
// 退订以前只从 map 删 ID，底层订阅不关，反复订阅会耗尽 worker 配额。
func Test隧道退订释放订阅配额(t *testing.T) {
	s, _, cwd := newTestServer(t)
	bridge := NewTunnelBridge(s, func(frame []byte) error { return nil }, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	start := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "u1",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", start)) {
		t.Fatal("隧道帧未被处理")
	}
	// 等 worker 真正起来，否则后面的会话 ID 取不到。
	waitFor(t, func() bool { return len(s.manager.List()) == 1 })
	workers := s.manager.List()
	sessionID := workers[0].SessionID
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %+v", workers[0])
	}

	// 反复订阅/退订：每次退订都必须真正释放 worker 侧配额。
	for i := 0; i < 12; i++ {
		id := "u-sub-" + strconv.Itoa(i)
		sub := mustFrame(t, map[string]any{
			"version": 1, "kind": "command", "requestId": id, "sessionId": sessionID,
			"method": "session.subscribe",
		})
		if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", sub)) {
			t.Fatalf("第 %d 次订阅未被处理", i)
		}
		unsub := mustFrame(t, map[string]any{
			"version": 1, "kind": "command", "requestId": id + "-x", "sessionId": sessionID,
			"method": "session.unsubscribe",
		})
		if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", unsub)) {
			t.Fatalf("第 %d 次退订未被处理", i)
		}
	}
	waitFor(t, func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		c, ok := bridge.virtual["tab-1"]
		return ok && len(c.subs) == 0
	})
	w, err := s.manager.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n := w.SubscriberCount(); n != 0 {
		t.Fatalf("退订后 worker 订阅数应为 0，实际 %d", n)
	}
}

// waitFor 轮询等待条件成立，避免测试依赖固定睡眠。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
