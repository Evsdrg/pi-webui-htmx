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
	"pi-bridge-go/internal/observe"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/workspace"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T) (*Server, *run.Manager, string) {
	t.Helper()
	cwd := t.TempDir()
	state := t.TempDir()
	sessionDir := filepath.Join(state, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(state, "agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspace.New([]string{cwd})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessions.New(sessionDir, policy, sessions.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fakePi, err := filepath.Abs(filepath.Join("..", "..", "testdata", "bin", "fake-pi"))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := storage.NewReceipts(filepath.Join(state, "receipts"), storage.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipts.Close() })
	metrics := observe.NewMetrics(observe.NewMethods(
		"worker.list", "session.start", "session.state", "session.prompt",
		"session.abort", "session.stop", "session.subscribe", "session.unsubscribe",
	))
	cfg := run.Defaults()
	cfg.Binary = fakePi
	cfg.AgentDir = agentDir
	cfg.Store = store
	cfg.Policy = policy
	cfg.MaxWorkers = 1
	cfg.IdleTimeout = 5 * time.Minute
	cfg.OperationTimeout = 2 * time.Second
	cfg.Metrics = metrics
	m := run.New(cfg)
	t.Cleanup(m.Close)

	files, err := workspace.NewFiles(policy, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Close)
	terminals := terminal.NewManager(terminal.Defaults())
	t.Cleanup(terminals.Close)

	return New(m, store, terminals, files, receipts, metrics, testToken, "127.0.0.1:30142"), m, cwd
}

func writeSessionFile(t *testing.T, dir, id, cwd string) {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func Test未授权请求被拒绝(t *testing.T) {
	s, _, _ := newTestServer(t)
	for _, path := range []string{"/api/v1/capabilities", "/api/v1/sessions", "/api/v1/ws"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:30142"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s 应返回 401，实际 %d", path, rec.Code)
		}
	}
}

func TestHost与Origin校验(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Host 不符应返回 403，实际 %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨源应返回 403，实际 %d", rec.Code)
	}
}

func Test健康检查不含敏感信息(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:30142"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "session") || strings.Contains(rec.Body.String(), "model") {
		t.Fatalf("健康检查泄露了额外信息: %s", rec.Body.String())
	}
}

func Test换取Cookie后可访问接口(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("换取 Cookie 失败: %d", rec.Code)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) != 1 || !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("Cookie 属性不符合要求: %+v", cookie)
	}
	next := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	next.Host = "127.0.0.1:30142"
	next.AddCookie(cookie[0])
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, next)
	if rec.Code != http.StatusOK {
		t.Fatalf("带 Cookie 访问失败: %d", rec.Code)
	}
}

func Test会话与历史查询不启动工作进程(t *testing.T) {
	s, m, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "sess-1", cwd)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=10", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("会话列表失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/history?limit=10", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("历史查询失败: %d %s", rec.Code, rec.Body.String())
	}
	if len(m.List()) != 0 {
		t.Fatalf("查询历史不得启动工作进程: %+v", m.List())
	}
}

func Test历史越界会话返回404(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/nope/history", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在会话应返回 404，实际 %d", rec.Code)
	}
}

func TestWS命令闭环与防重(t *testing.T) {
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

	send(map[string]any{"version": 1, "kind": "command", "requestId": "r1", "method": "worker.list"})
	if m := read(); m["kind"] != "response" || m["ok"] != true {
		t.Fatalf("worker.list 响应异常: %v", m)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r2", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	if started["ok"] != true {
		t.Fatalf("启动失败: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}
	// 回执存储启用时，重复 requestId 直接回放结论，绝不重新执行。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r2", "method": "session.stop"})
	m := read()
	if m["ok"] != true {
		t.Fatalf("重复 requestId 应回放结论: %v", m)
	}
	if data, _ := m["data"].(map[string]any); data["duplicate"] != true {
		t.Fatalf("应标记为重复执行: %v", m)
	}
	if len(s.manager.List()) != 1 {
		t.Fatalf("不得重复启动工作进程: %+v", s.manager.List())
	}
	// 协议层被拒的命令不落「已执行」回执，客户端可重试。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r9", "method": "session.compact"})
	if m := read(); m["ok"] != false {
		t.Fatalf("未实现方法应被拒绝: %v", m)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r9", "method": "worker.list"})
	if m := read(); m["ok"] != true {
		t.Fatalf("被拒的 requestId 应可重试: %v", m)
	}
	// 未启动的会话操作应返回 worker_not_running，而不是隐式启动。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r3", "sessionId": "unknown", "method": "session.state"})
	if m := read(); m["ok"] != false {
		t.Fatalf("未启动会话应失败: %v", m)
	}
}

func TestWS未实现方法被明确拒绝(t *testing.T) {
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
		_ = conn.Write(ctx, websocket.MessageText, b)
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
	send(map[string]any{"version": 1, "kind": "command", "requestId": "s1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	_ = read()
	for _, method := range []string{"session.export_html", "session.import", "session.share", "session.reload", "worker.stop_all", "files.write", "files.delete"} {
		send(map[string]any{"version": 1, "kind": "command", "requestId": "m-" + method, "sessionId": "fake-session", "method": method})
		m := read()
		if m["ok"] != false {
			t.Fatalf("%s 应被拒绝: %v", method, m)
		}
		if code, _ := m["error"].(map[string]any)["code"].(string); code != "unsupported_method" {
			t.Fatalf("%s 错误码应为 unsupported_method，实际 %v", method, m["error"])
		}
	}
}

func TestMetrics端点需鉴权且不含密钥(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.Host = "127.0.0.1:30142"
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未授权应返回 401，实际 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("指标查询失败: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, testToken) {
		t.Fatal("指标响应不得包含令牌")
	}
	for _, key := range []string{"commandsTotal", "byMethod", "sessions", "receipts", "workers"} {
		if !strings.Contains(body, key) {
			t.Fatalf("指标缺少 %s: %s", key, body)
		}
	}
}

func Test历史请求计入指标(t *testing.T) {
	s, _, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "sess-m", cwd)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-m/history", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("历史查询失败: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"historyRequests":1`) {
		t.Fatalf("历史请求未计入指标: %s", rec.Body.String())
	}
}

func Test重复requestId跨连接不重复执行(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		header := http.Header{}
		header.Set("Authorization", "Bearer "+testToken)
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatalf("连接失败: %v", err)
		}
		return conn
	}
	send := func(conn *websocket.Conn, v map[string]any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func(conn *websocket.Conn) map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}

	first := dial()
	defer first.CloseNow()
	send(first, map[string]any{"version": 1, "kind": "command", "requestId": "dup-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	if m := read(first); m["ok"] != true {
		t.Fatalf("首次启动失败: %v", m)
	}
	first.CloseNow()

	// 新连接使用同一 requestId：必须回放结论而不是再拉一个进程。
	second := dial()
	defer second.CloseNow()
	send(second, map[string]any{"version": 1, "kind": "command", "requestId": "dup-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	m := read(second)
	data, _ := m["data"].(map[string]any)
	if data == nil || data["duplicate"] != true {
		t.Fatalf("应回放命令结论: %v", m)
	}
	if data["outcome"] != "ok" {
		t.Fatalf("回放结论应为 ok: %v", m)
	}
	if len(s.manager.List()) != 1 {
		t.Fatalf("不得重复启动工作进程: %+v", s.manager.List())
	}
}

func Test事件补发与失效(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		header := http.Header{}
		header.Set("Authorization", "Bearer "+testToken)
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatalf("连接失败: %v", err)
		}
		return conn
	}
	send := func(conn *websocket.Conn, v map[string]any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	// readResponse 跳过事件与控制帧，只取指定 requestId 的响应。
	readResponse := func(conn *websocket.Conn, requestID string) map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			_, b, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m["kind"] == "response" && m["requestId"] == requestID {
				return m
			}
		}
		t.Fatalf("未收到 %s 的响应", requestID)
		return nil
	}

	conn := dial()
	defer conn.CloseNow()
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "rp-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := readResponse(conn, "rp-1")
	if started["ok"] != true {
		t.Fatalf("启动失败: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	id, _ := data["sessionId"].(string)
	epoch, _ := data["epoch"].(string)
	if id == "" || epoch == "" {
		t.Fatalf("启动响应缺少身份信息: %v", started)
	}
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "rp-2", "sessionId": id, "method": "session.subscribe"})
	sub := readResponse(conn, "rp-2")
	if sub["ok"] != true {
		t.Fatalf("订阅失败: %v", sub)
	}
	subData, _ := sub["data"].(map[string]any)
	seq, _ := subData["seq"].(float64)
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "rp-3", "sessionId": id, "method": "session.prompt", "params": map[string]any{"text": "hi"}})
	if m := readResponse(conn, "rp-3"); m["ok"] != true {
		t.Fatalf("发送失败: %v", m)
	}
	conn.CloseNow()

	// 同 epoch + 旧序号可以补发。
	replay := dial()
	defer replay.CloseNow()
	send(replay, map[string]any{"version": 1, "kind": "command", "requestId": "rp-4", "sessionId": id, "method": "session.subscribe",
		"params": map[string]any{"epoch": epoch, "afterSeq": uint64(seq)}})
	if m := readResponse(replay, "rp-4"); m["ok"] != true {
		t.Fatalf("带游标订阅应成功: %v", m)
	}
	// 错误的 epoch 必须要求重新同步，而不是假装补齐。
	bad := dial()
	defer bad.CloseNow()
	send(bad, map[string]any{"version": 1, "kind": "command", "requestId": "rp-5", "sessionId": id, "method": "session.subscribe",
		"params": map[string]any{"epoch": "不存在的epoch", "afterSeq": 0}})
	m := readResponse(bad, "rp-5")
	if m["ok"] != false {
		t.Fatalf("epoch 不匹配应失败: %v", m)
	}
	if code, _ := m["error"].(map[string]any)["code"].(string); code != "resync_required" {
		t.Fatalf("错误码应为 resync_required，实际 %v", m["error"])
	}
}

func Test连接内防重在回执不可用时仍生效(t *testing.T) {
	s, _, cwd := newTestServer(t)
	// 模拟回执存储不可用：连接内 seen 表必须独立挡住重复执行。
	s.receipts = nil
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
		t.Fatal(err)
	}
	defer conn.CloseNow()
	send := func(v map[string]any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "n1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	if m := read(); m["ok"] != true {
		t.Fatalf("启动失败: %v", m)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "n1", "method": "session.stop", "params": map[string]any{"force": true}})
	dup := read()
	if dup["ok"] != false {
		t.Fatalf("回执不可用时连接内防重应生效: %v", dup)
	}
	if code, _ := dup["error"].(map[string]any)["code"].(string); code != "conflict" {
		t.Fatalf("错误码应为 conflict，实际 %v", dup["error"])
	}
}
