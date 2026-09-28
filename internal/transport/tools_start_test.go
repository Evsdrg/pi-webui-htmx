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

// dial 建立一条带令牌的 WS 连接，返回发送/读取助手。
func dial(t *testing.T, s *Server) (*websocket.Conn, func(any), func() map[string]any) {
	t.Helper()
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
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
		_ = json.Unmarshal(b, &m)
		return m
	}
	return conn, send, read
}

func TestSessionStart拒绝未知工具预设(t *testing.T) {
	s, _, cwd := newTestServer(t)
	_, send, read := dial(t, s)
	send(map[string]any{"version": 1, "kind": "command", "requestId": "t1", "method": "session.start", "params": map[string]any{"cwd": cwd, "toolPreset": "admin"}})
	m := read()
	if m["ok"] != false {
		t.Fatalf("未知工具预设应被拒绝: %v", m)
	}
	err, _ := m["error"].(map[string]any)
	if code, _ := err["code"].(string); code != "invalid_params" {
		t.Fatalf("错误码应为 invalid_params: %v", m)
	}
	if msg, _ := err["message"].(string); !strings.Contains(msg, "工具预设") {
		t.Fatalf("错误信息应说明原因: %v", m)
	}
	// 拒绝后不能留下任何工作进程。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "t2", "method": "worker.list"})
	list := read()
	data, _ := list["data"].([]any)
	if len(data) != 0 {
		t.Fatalf("拒绝后不应留下工作进程: %v", list)
	}
}

func TestSessionStart接受受支持的工具预设(t *testing.T) {
	for _, preset := range []string{"", "default", "chat-only", "read-only", "full"} {
		t.Run("预设 "+preset, func(t *testing.T) {
			s, _, cwd := newTestServer(t)
			_, send, read := dial(t, s)
			send(map[string]any{"version": 1, "kind": "command", "requestId": "s1", "method": "session.start", "params": map[string]any{"cwd": cwd, "toolPreset": preset}})
			m := read()
			if m["ok"] != true {
				t.Fatalf("预设 %q 应被接受: %v", preset, m)
			}
			data, _ := m["data"].(map[string]any)
			if id, _ := data["sessionId"].(string); id == "" {
				t.Fatalf("启动响应缺少 sessionId: %v", m)
			}
			if preset != "" && preset != "default" {
				if got, _ := data["toolPreset"].(string); got != preset {
					t.Fatalf("启动响应应回带工具预设 %q，实际 %q", preset, got)
				}
			}
		})
	}
}
