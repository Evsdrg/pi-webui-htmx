package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Test终端尺寸上限对resize同样生效 覆盖 B79：
// Open 会把超限尺寸夹到 MaxCols/MaxRows，Resize 以前只拒绝 0，
// 于是 65535×65535 会被原样交给 PTY，让它按这个尺寸分配渲染缓冲。
func Test终端尺寸上限对resize同样生效(t *testing.T) {
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

	open := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "open",
		"method": "terminal.open", "params": map[string]any{"cwd": cwd, "cols": 80, "rows": 24},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", open))
	reply := waitReply(t, &mu, &sent, "open")
	data, _ := reply["data"].(map[string]any)
	id, _ := data["terminalId"].(string)
	if id == "" {
		t.Fatalf("终端未打开: %v", reply)
	}

	resize := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "resize",
		"method": "terminal.resize",
		"params": map[string]any{"terminalId": id, "cols": 65535, "rows": 65535},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", resize))
	if reply := waitReply(t, &mu, &sent, "resize"); reply["ok"] != true {
		t.Fatalf("超限 resize 应当夹紧而不是报错: %v", reply)
	}

	list := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "list", "method": "terminal.list",
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", list))
	reply = waitReply(t, &mu, &sent, "list")
	data, _ = reply["data"].(map[string]any)
	terms, _ := data["terminals"].([]any)
	if len(terms) != 1 {
		t.Fatalf("应只有一个终端: %v", reply)
	}
	term, _ := terms[0].(map[string]any)
	if cols, _ := term["cols"].(float64); cols != 500 {
		t.Fatalf("cols 应夹到上限 500，实际 %v", term["cols"])
	}
	if rows, _ := term["rows"].(float64); rows != 200 {
		t.Fatalf("rows 应夹到上限 200，实际 %v", term["rows"])
	}
}
