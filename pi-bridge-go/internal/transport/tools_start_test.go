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

// TestSessionStart忽略历史工具预设字段 工作进程现在始终启用全部工具。
// 旧客户端仍会发 toolPreset，必须被容忍（不因 DisallowUnknownFields 报错），
// 但该字段不再影响行为，启动响应也不再回带它。
func TestSessionStart忽略历史工具预设字段(t *testing.T) {
	for _, preset := range []string{"", "default", "chat-only", "read-only", "full", "admin"} {
		t.Run("字段值 "+preset, func(t *testing.T) {
			s, _, cwd := newTestServer(t)
			_, send, read := dial(t, s)
			send(map[string]any{"version": 1, "kind": "command", "requestId": "s1", "method": "session.start", "params": map[string]any{"cwd": cwd, "toolPreset": preset}})
			m := read()
			if m["ok"] != true {
				t.Fatalf("历史字段 %q 应被忽略而非拒绝: %v", preset, m)
			}
			data, _ := m["data"].(map[string]any)
			if id, _ := data["sessionId"].(string); id == "" {
				t.Fatalf("启动响应缺少 sessionId: %v", m)
			}
			if _, exists := data["toolPreset"]; exists {
				t.Fatalf("启动响应不应再回带 toolPreset: %v", m)
			}
		})
	}
}
