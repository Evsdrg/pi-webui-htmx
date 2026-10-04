// 删除忙会话的 force 传导与会话内跳转的端到端回归。
//
// 两处都是「桥已经声明、但从未传导到 pi」的接线缺口：
//   - sessions.delete 的 force 从未传给 Worker.Stop，忙会话即使带 force
//     也删不掉（B08 的强制分支形同虚设）；
//   - session.navigate 是新增通道：prompt 文本命中桥内扩展命令，
//     结果经结果文件回传，回执带新叶子。
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
)

// dialTestWS 连接测试服务器的 WS 入口，返回收发闭包。
func dialTestWS(t *testing.T, s *Server) (func(any), func() map[string]any) {
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
		t.Fatalf("WS 连接失败: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
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
	return send, read
}

// startWorker 经 WS 启动一个 worker，返回会话 ID。
func startWorker(t *testing.T, send func(any), read func() map[string]any, cwd string) string {
	t.Helper()
	send(map[string]any{"version": 1, "kind": "command", "requestId": "start-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	if started["ok"] != true {
		t.Fatalf("启动失败: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	id, _ := data["sessionId"].(string)
	if id == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}
	return id
}

// Test删除忙会话必须强制：不带 force 被拒且状态不变；带 force 停 worker 并删文件。
func Test删除忙会话必须强制(t *testing.T) {
	s, m, cwd, store, sessionDir := newTestServerTuned(t, 5*time.Second)
	send, read := dialTestWS(t, s)
	id := startWorker(t, send, read, cwd)

	// 会话文件与索引（模拟 Pi 已落盘的会话）。
	writeSessionFile(t, sessionDir, id, cwd)
	if _, err := store.List(context.Background(), 0, 10, ""); err != nil {
		t.Fatal(err)
	}

	// 「触发对话」让夹具发出扩展 UI 请求并悬置：worker 进入忙状态。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "p1", "sessionId": id, "method": "session.prompt", "params": map[string]any{"text": "触发对话"}})
	if m := read(); m["ok"] != true {
		t.Fatalf("prompt 失败: %v", m)
	}

	// 非强制删除：必须被拒（busy），且 worker 与文件都原样保留。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "d1", "method": "sessions.delete", "params": map[string]any{"sessionId": id}})
	if m := read(); m["ok"] != false || replyCode(m) != "busy" {
		t.Fatalf("忙会话的非强制删除应报 busy: %v", m)
	}
	if _, err := m.Get(id); err != nil {
		t.Fatalf("被拒的删除不应动 worker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, id+".jsonl")); err != nil {
		t.Fatalf("被拒的删除不应动文件: %v", err)
	}

	// 强制删除：停掉忙 worker 并删文件。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "d2", "method": "sessions.delete", "params": map[string]any{"sessionId": id, "force": true}})
	deleted := read()
	if deleted["ok"] != true {
		t.Fatalf("强制删除失败: %v", deleted)
	}
	if data, _ := deleted["data"].(map[string]any); data["stoppedWorker"] != true {
		t.Fatalf("强制删除应标记 stoppedWorker: %v", deleted)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, id+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("会话文件应已删除: %v", err)
	}
	// 表项清理由退出协程完成，与 Stop 返回之间隔着一个调度窗口：轮询等待。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := m.Get(id); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("强制删除后 worker 应已清理")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWS跳转会话语义：user 消息回到父节点、非 user 目标落在自身；忙时拒绝。
func TestWS跳转会话语义(t *testing.T) {
	s, _, cwd, _, _ := newTestServerTuned(t, 5*time.Second)
	send, read := dialTestWS(t, s)
	id := startWorker(t, send, read, cwd)

	send(map[string]any{"version": 1, "kind": "command", "requestId": "n1", "sessionId": id, "method": "session.navigate", "params": map[string]any{"entryId": "u1"}})
	m := read()
	if m["ok"] != true {
		t.Fatalf("跳转失败: %v", m)
	}
	if data, _ := m["data"].(map[string]any); data["leafId"] != "" || data["previousLeafId"] != "a1" {
		t.Fatalf("用户消息应回到父节点（根为空串）: %v", m)
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "n2", "sessionId": id, "method": "session.navigate", "params": map[string]any{"entryId": "a1"}})
	m = read()
	if m["ok"] != true {
		t.Fatalf("跳转失败: %v", m)
	}
	if data, _ := m["data"].(map[string]any); data["leafId"] != "a1" {
		t.Fatalf("非用户目标应落在目标自身: %v", m)
	}

	// 不存在的目标：pi 的错误经 pi_error 回传，前端可展示。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "n3", "sessionId": id, "method": "session.navigate", "params": map[string]any{"entryId": "zz"}})
	if m := read(); m["ok"] != false || replyCode(m) != "pi_error" {
		t.Fatalf("不存在的目标应报 pi_error: %v", m)
	}

	// 运行中的会话拒绝跳转（先让 worker 忙起来）。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "p1", "sessionId": id, "method": "session.prompt", "params": map[string]any{"text": "触发对话"}})
	if m := read(); m["ok"] != true {
		t.Fatalf("prompt 失败: %v", m)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "n4", "sessionId": id, "method": "session.navigate", "params": map[string]any{"entryId": "u1"}})
	if m := read(); m["ok"] != false || replyCode(m) != "busy" {
		t.Fatalf("忙会话应拒绝跳转: %v", m)
	}
}
