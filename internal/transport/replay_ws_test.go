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

func TestWS批量补发后连接仍可用(t *testing.T) {
	t.Setenv("FAKE_PI_SCRIPT", "replay_burst")
	s, manager, cwd := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	worker, err := manager.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	initial := worker.Info()
	if err := worker.Prompt(ctx, "生成补发测试事件", "", nil); err != nil {
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
			t.Fatalf("补发过程中连接意外断开: %v", err)
		}
		var message map[string]any
		if err := json.Unmarshal(b, &message); err != nil {
			t.Fatal(err)
		}
		return message
	}

	send("replay-burst", "session.subscribe", map[string]any{"epoch": initial.Epoch, "afterSeq": initial.Seq})
	var count int
	last := initial.Seq
	confirmed := false
	for count < 122 || !confirmed {
		message := read()
		switch message["kind"] {
		case "event":
			seq := uint64(message["seq"].(float64))
			if seq != last+1 {
				t.Fatalf("补发序号不连续: %d → %d", last, seq)
			}
			last = seq
			count++
		case "response":
			if message["requestId"] == "replay-burst" {
				if message["ok"] != true {
					t.Fatalf("订阅未确认: %v", message["error"])
				}
				confirmed = true
			}
		}
	}
	send("after-replay", "session.state", map[string]any{})
	for {
		message := read()
		if message["requestId"] == "after-replay" {
			if message["ok"] != true {
				t.Fatalf("补发后同一连接不能继续操作: %v", message["error"])
			}
			break
		}
	}
}
