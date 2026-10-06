package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/storage"
)

// 订阅命令自己已经发出确认响应，框架不能再补一条：同一 requestId 只应
// 有一条 response（v1 契约）。
func Test订阅命令只回一条响应(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
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
		_ = json.Unmarshal(b, &m)
		return m
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "start", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	data, _ := started["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "sub", "sessionId": sessionID, "method": "session.subscribe"})
	// 收集一小段时间内该 requestId 的全部响应。
	var responses []map[string]any
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rctx, rcancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, b, err := conn.Read(rctx)
		rcancel()
		if err != nil {
			break
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m["kind"] == "response" && m["requestId"] == "sub" {
			responses = append(responses, m)
		}
	}
	if len(responses) != 1 {
		t.Fatalf("订阅命令应恰好一条响应，实际 %d 条: %v", len(responses), responses)
	}
	if d, _ := responses[0]["data"].(map[string]any); d["subscribed"] != true {
		t.Fatalf("订阅响应应带 subscribed:true: %v", responses[0])
	}
}

// 响应帧经 JSON 转义后超过单帧上限时，必须显式截断/报错，而不是拆掉整条
// 连接——否则同连接上其它在飞命令的结论会一并丢失。
func Test超大响应不拆连接(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// coder/websocket 的客户端默认读上限只有 32 KiB，浏览器没有这个限制；
	// 这里放宽到能收下桥允许的最大帧，才能真正验证「连接不被拆掉」。
	conn.SetReadLimit(4 << 20)

	// 450 KiB 全是反斜杠：JSON 转义后约 900 KiB，超过 512 KiB 的帧上限。
	path := filepath.Join(cwd, "slashes.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat(`\`, 450<<10)), 0644); err != nil {
		t.Fatal(err)
	}

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("连接在读响应前被拆掉: %v", err)
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("响应不是 JSON: %s", b)
		}
		return m
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "read", "method": "files.read", "params": map[string]any{"path": path}})
	resp := read()
	if resp["kind"] != "response" || resp["requestId"] != "read" {
		t.Fatalf("超大响应应得到一条明确响应: %v", resp)
	}
	if resp["ok"] == true {
		d, _ := resp["data"].(map[string]any)
		if d["truncated"] != true {
			t.Fatalf("超大响应应标记 truncated: %v", resp)
		}
	} else if code := replyCode(resp); code != "limit_exceeded" {
		t.Fatalf("失败时应是 limit_exceeded，实际 %q", code)
	}
	// 连接必须仍然可用。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "alive", "method": "worker.list"})
	if m := read(); m["requestId"] != "alive" || m["ok"] != true {
		t.Fatalf("连接不应被拆掉: %v", m)
	}
}

// 变更命令超时（outcome_unknown）必须记成 unknown 回执，而不是 error：
// error 会被客户端读成「确定没执行」而重发一个可能已执行的动作。
func Test超时回执记为unknown(t *testing.T) {
	if got := outcomeFor(protocol.E("outcome_unknown", "Pi 可能已接受该命令")); got != storage.OutcomeUnknown {
		t.Fatalf("outcome_unknown 应记成 unknown，实际 %q", got)
	}
	// 重放该回执时回答 outcome_unknown，而不是假装成功。
	s, _, _ := newTestServer(t)
	s.storeReceipt(storage.Receipt{RequestID: "x1", Method: "session.prompt", Outcome: storage.OutcomeUnknown})
	req := protocol.Request{Version: 1, Kind: "command", RequestID: "x1", Method: "session.prompt"}
	accepted, _ := s.admit(req, func(m protocol.Message) {
		if m.Error == nil || m.Error.Code != "outcome_unknown" {
			t.Fatalf("应回答 outcome_unknown，实际 %+v", m)
		}
	})
	if accepted {
		t.Fatal("unknown 回执不应被当作可执行命令")
	}
}
