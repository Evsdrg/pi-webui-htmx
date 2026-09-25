package transport

import (
	"context"
	"encoding/json"
	"sync"
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
