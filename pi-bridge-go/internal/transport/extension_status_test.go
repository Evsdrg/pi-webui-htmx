package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// B36：扩展状态行快照挂在 worker 上，按会话取。
// 老实现是传输层的全局 map，只在把事件转发给浏览器的路径上更新——
// 于是没有订阅者时漏记，且不同会话的同名 key 互相覆盖。
func Test扩展状态快照不依赖订阅且按会话取(t *testing.T) {
	// fake Pi 在启动时先发一条 setStatus，模拟插件的一次性状态行。
	t.Setenv("FAKE_PI_SCRIPT", "set_status")
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	defer conn.CloseNow()

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("响应不是 JSON: %s", b)
		}
		return m
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "e1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	startData, _ := started["data"].(map[string]any)
	sessionID, _ := startData["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}
	// 全程没有任何订阅：快照仍必须有内容。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "e2", "sessionId": sessionID, "method": "session.ext_status"})
	reply := read()
	if reply["ok"] != true {
		t.Fatalf("ext_status 失败: %v", reply)
	}
	body, _ := reply["data"].(map[string]any)
	statuses, _ := body["statuses"].(map[string]any)
	if statuses["mc"] != "mc: 3 (1%) · idle" {
		t.Fatalf("无订阅者也应有快照: %v", reply)
	}
	if body["epoch"] == "" {
		t.Fatalf("快照应带 epoch: %v", reply)
	}

	// 未启动 worker 的会话：RPC 明确拒绝，不能串到别人的状态。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "e3", "sessionId": "sess-other", "method": "session.ext_status"})
	if other := read(); other["ok"] != false {
		t.Fatalf("未启动的会话应被拒绝: %v", other)
	}
}
