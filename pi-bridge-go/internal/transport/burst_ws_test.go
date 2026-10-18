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

// TestWS增量突发后连接仍可用 是慢消费者路径的端到端反例：
// 假 Pi 连发 120 条 text_delta（可丢增量），订阅端只读一小部分，
// 连接必须**保持可用**——旧的「积压即摘除」会把整条连接踢掉。
func TestWS增量突发后连接仍可用(t *testing.T) {
	t.Setenv("FAKE_PI_SCRIPT", "replay_burst")
	s, manager, cwd := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	worker, err := manager.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	initial := worker.Info()
	if err := worker.Prompt(ctx, "生成突发测试事件", "", nil); err != nil {
		t.Fatal(err)
	}
	for worker.Info().Seq < initial.Seq+122 {
		if ctx.Err() != nil {
			t.Fatalf("假 Pi 未完成事件发布: %v", ctx.Err())
		}
		time.Sleep(2 * time.Millisecond)
	}

	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	send := func(id, method string, params any) {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"version": 1, "kind": "command", "requestId": id,
			"sessionId": initial.SessionID, "method": method, "params": params,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("突发过程中连接意外断开: %v", err)
		}
		var message map[string]any
		if err := json.Unmarshal(b, &message); err != nil {
			t.Fatal(err)
		}
		return message
	}

	// 订阅确认必须回来，且不再声明 replay。
	send("burst-subscribe", "session.subscribe", map[string]any{})
	for {
		message := read()
		if message["kind"] == "response" && message["requestId"] == "burst-subscribe" {
			if message["ok"] != true {
				t.Fatalf("订阅未确认: %v", message["error"])
			}
			data, _ := message["data"].(map[string]any)
			if _, ok := data["replay"]; ok {
				t.Fatalf("订阅确认不应再声明 replay: %v", data)
			}
			break
		}
	}
	// 再发一批事件，确认同一连接仍能收发。
	if err := worker.Prompt(ctx, "第二批突发", "", nil); err != nil {
		t.Fatal(err)
	}
	send("after-burst", "session.state", map[string]any{})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		message := read()
		if message["requestId"] == "after-burst" {
			if message["ok"] != true {
				t.Fatalf("突发后同一连接不能继续操作: %v", message["error"])
			}
			return
		}
	}
	t.Fatal("突发后未收到 session.state 响应")
}
