package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/andybalholm/brotli"
	"github.com/coder/websocket"
	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/observe"
	"pi-bridge-go/internal/pi"
	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/testutil"
	"pi-bridge-go/internal/workspace"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T) (*Server, *run.Manager, string) {
	t.Helper()
	return newTestServerTuned(t, 2*time.Second)
}

// newTestServerTuned 与 newTestServer 相同，只是把命令超时压到指定值，
// 并允许改终端限额（能力发现的用例要证明报的是真实值而不是默认值）。
// B66 的用例需要默认超时在几百毫秒内到期，才能区分长任务命令用的不是它。
func newTestServerTuned(t *testing.T, commandTimeout time.Duration, termOpts ...func(*terminal.Config)) (*Server, *run.Manager, string) {
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
	fakePi, err := testutil.FakePi()
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
	cfg.OperationTimeout = commandTimeout
	cfg.Metrics = metrics
	m := run.New(cfg)
	t.Cleanup(m.Close)

	files, err := workspace.NewFiles(policy, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Close)
	termCfg := terminal.Defaults()
	for _, opt := range termOpts {
		opt(&termCfg)
	}
	terminals := terminal.NewManager(termCfg)
	t.Cleanup(terminals.Close)

	piConfig := management.NewConfig(agentDir, management.DefaultLimits())
	exportDir := filepath.Join(state, "exports")
	if err := os.MkdirAll(exportDir, 0700); err != nil {
		t.Fatal(err)
	}
	// UI 包目录由 testutil 解析：环境变量优先，否则按单仓布局推断
	// （仓库根的 pi-webui-htmx）。找不到时 UI 层禁用（nil）——那是合法状态；
	// 目录存在但加载失败会让测试失败（见 ui_dir_test.go）。
	ui := uiRenderer(t)
	srv, err := New(Options{
		Manager: m, Store: store, Terminals: terminals, Files: files,
		Config: piConfig, Discovery: management.DefaultDiscoveryLimits(),
		ExportDir: exportDir, Receipts: receipts, Metrics: metrics,
		Token: testToken, Host: "127.0.0.1:30142", UI: ui,
	})
	if err != nil {
		t.Fatalf("构造测试服务器失败：%v", err)
	}
	return srv, m, cwd
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
	// 必须用相同的方法与参数：同一 requestId 配不同内容是客户端错误，
	// 只能报 conflict，不能回放另一条命令的结论。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r2", "method": "session.start", "params": map[string]any{"cwd": cwd}})
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
	// 同一 requestId 换内容必须被拒绝，不能静默复用旧结论。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r2", "method": "session.stop"})
	if m := read(); m["ok"] != false {
		t.Fatalf("同一 requestId 配不同命令应被拒绝: %v", m)
	}
	// 协议层被拒的命令不落「已执行」回执，客户端可重试。
	// 重试必须用同一 requestId 重发同一条命令；换内容是客户端错误。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r9", "method": "session.compact"})
	first := read()
	if first["ok"] != false {
		t.Fatalf("未启动会话的命令应被拒绝: %v", first)
	}
	if code := replyCode(first); code == "conflict" {
		t.Fatalf("首次失败不应是 conflict: %v", first)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r9", "method": "session.compact"})
	again := read()
	if code := replyCode(again); code == "conflict" {
		t.Fatalf("被拒的 requestId 应可重试: %v", again)
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
	for _, method := range []string{"session.import", "session.share", "session.reload", "worker.stop_all", "files.write", "files.delete", "packages.install"} {
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

func Test能力清单与实际分发一致(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	methods, _ := out["methods"].([]any)
	if len(methods) != len(SupportedMethods) {
		t.Fatalf("能力清单数量与 SupportedMethods 不一致: %d vs %d", len(methods), len(SupportedMethods))
	}
	// 每个声明支持的方法都不应返回 unsupported_method。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	read := func(requestID string) map[string]any {
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
	send(map[string]any{"version": 1, "kind": "command", "requestId": "cap-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read("cap-1")
	if started["ok"] != true {
		t.Fatalf("启动失败: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	id, _ := data["sessionId"].(string)
	for i, raw := range methods {
		method, _ := raw.(string)
		reqID := "cap-m-" + string(rune('a'+i))
		params := map[string]any{}
		switch method {
		case "session.subscribe", "session.state", "session.stop":
			params = map[string]any{}
		case "config.catalog", "config.packages":
			// 此测试只核对分发，不应访问公网；具体查询另有隔离测试。
			params = map[string]any{"invalidTestField": true}
		}
		send(map[string]any{"version": 1, "kind": "command", "requestId": reqID, "sessionId": id, "method": method, "params": params})
		m := read(reqID)
		if m["ok"] == false {
			if code, _ := m["error"].(map[string]any)["code"].(string); code == "unsupported_method" {
				t.Fatalf("能力清单声明支持 %s，实际却未实现", method)
			}
		}
	}
}

func Test模型配置HTTP端点只接受GET(t *testing.T) {
	s, _, _ := newTestServer(t)
	// 配置写入只走 WS（带 requestId 防重与回执），HTTP 侧不提供 PUT/POST。
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/config/models", strings.NewReader("{}"))
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应返回 405，实际 %d", method, rec.Code)
		}
	}
}

func Test配置写入拒绝损坏文档(t *testing.T) {
	dir := t.TempDir()
	cfg := management.NewConfig(dir, management.DefaultLimits())
	for name, doc := range map[string]map[string]any{
		"缺 providers": {"x": 1},
		// Pi 的 api 只是非空字符串，"openai-completions" 合法；只拦空串。
		"api 为空":     {"providers": map[string]any{"p": map[string]any{"api": ""}}},
		"模型缺 id":     {"providers": map[string]any{"p": map[string]any{"models": []any{map[string]any{"name": "无 id"}}}}},
		"models 非数组": {"providers": map[string]any{"p": map[string]any{"models": map[string]any{"m": nil}}}},
	} {
		if err := cfg.WriteModels(doc); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
}

func Test发现接口拒绝非法URL与头部(t *testing.T) {
	cfg := management.NewConfig(t.TempDir(), management.DefaultLimits())
	if _, err := cfg.Discover(context.Background(), "ftp://x", "openai-completions", "", nil, management.DefaultDiscoveryLimits()); err == nil {
		t.Fatal("非 http(s) 必须被拒绝")
	}
	if _, err := cfg.Discover(context.Background(), "", "openai-completions", "", nil, management.DefaultDiscoveryLimits()); err == nil {
		t.Fatal("空 baseURL 必须被拒绝")
	}
	if _, err := cfg.Discover(context.Background(), "https://x.example", "openai-completions", "",
		map[string]string{"X-Bad": "a\r\nX-Injected: 1"}, management.DefaultDiscoveryLimits()); err == nil {
		t.Fatal("头部注入必须被拒绝")
	}
}

func Test活跃未落盘会话历史明确返回空状态(t *testing.T) {
	requireUI(t)
	s, manager, cwd := newTestServer(t)
	worker, err := manager.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	id := worker.Info().SessionID
	read := func(id, suffix string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/ui/sessions/"+id+"/history"+suffix, nil)
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	unsaved := read(id, "")
	if unsaved.Code != http.StatusNoContent || unsaved.Header().Get("X-Session-Unsaved") != "1" || unsaved.Body.Len() != 0 {
		t.Fatalf("活跃但未写盘的会话应回 204 且无伪造历史: code=%d header=%q body=%s", unsaved.Code, unsaved.Header().Get("X-Session-Unsaved"), unsaved.Body.String())
	}
	missing := read("does-not-exist", "")
	if missing.Code == http.StatusNoContent {
		t.Fatal("没有活跃 worker 的缺失会话不得伪装为临时会话")
	}
	writeSessionFile(t, s.store.Dir(), id, cwd)
	if got := read(id, ""); got.Code != http.StatusOK || got.Header().Get("X-Session-Unsaved") != "" {
		t.Fatalf("写盘后应读取真实历史: %d %s", got.Code, got.Body.String())
	}
	if got := read(id, "?leafId=not-a-leaf"); got.Code == http.StatusNoContent {
		t.Fatal("持久会话的非法叶子不得被当作未落盘")
	}
}

func TestUI端点返回滚动模式(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	// 造一个会话文件。历史读取不应启动 worker。
	writeSessionFile(t, s.store.Dir(), "sc1", cwd)

	// 首屏（无 before/leaf）应为 append。
	req := httptest.NewRequest(http.MethodGet, "/ui/sessions/sc1/history", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("首屏历史应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Scroll-Mode"); got != "append" {
		t.Fatalf("首屏应为 append，实际 %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("应返回 text/html，实际 %q", ct)
	}

	// 带 before 的翻页应为 prepend。
	req2 := httptest.NewRequest(http.MethodGet, "/ui/sessions/sc1/history?before=some-entry-id", nil)
	req2.Host = "127.0.0.1:30142"
	req2.Header.Set("Authorization", "Bearer "+testToken)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("X-Scroll-Mode"); got != "prepend" {
		t.Fatalf("翻页应为 prepend，实际 %q", got)
	}
}

func Test静态资源拒绝路径穿越(t *testing.T) {
	s, _, _ := newTestServer(t)
	for _, bad := range []string{"/assets/", "/assets/a/b.js", "/assets/app.js%00.css"} {
		req := httptest.NewRequest(http.MethodGet, bad, nil)
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("%q 不应返回资源", bad)
		}
	}
	// 不存在的静态文件无论是否登录都为 404；真实静态资源用于登录页。
	req := httptest.NewRequest(http.MethodGet, "/assets/app-abc.js", nil)
	req.Host = "127.0.0.1:30142"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失资源应 404，实际 %d", rec.Code)
	}
}

// Test协商编码与实际压缩器一致 是端到端回归。
// 曾把 gzip 流标成 Content-Encoding: br 发出，浏览器全部解不开，
// 而单测只覆盖 gzip 所以漏掉了。这里按响应头选解析器，
// 解不开或解出来不对都算失败。
func Test协商编码与实际压缩器一致(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// 一个最小但合法的 UI 包：只用到 sessions 模板。
	write("src/templates/sessions.html", `{{range .Items}}<a class="session-item" href="/?session={{.ID}}" data-session="{{.ID}}"><span class="session-title">{{.Title}}</span><span class="session-meta">{{.Modified}} {{.Cwd}}</span></a>{{end}}`)
	write("ui-manifest.json", `{"protocolVersion":1,"requiredMethods":[],"templates":{"sessions":"templates/sessions.html"},"build":{"entry":"src/entry/app.ts"}}`)
	write("dist/.vite/manifest.json", `{"src/entry/app.ts":{"file":"assets/app-abc123.js","isEntry":true,"css":[]}}`)
	write("dist/assets/app-abc123.js", "console.log(1)")
	t.Setenv("PI_WEBUI_DIR", dir)
	s, _, cwd := newTestServer(t)

	// 造足够多的会话，让列表片段超过压缩阈值。
	// cwd 必须用服务器自己那一个：策略只允许它，别处的会话会被过滤掉。
	for i := 0; i < 60; i++ {
		writeSessionFile(t, s.store.Dir(), "sess-"+strconv.Itoa(i), cwd)
	}
	for _, accept := range []string{"br", "gzip", "br, gzip", "gzip, br", ""} {
		name := accept
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ui/sessions", nil)
			req.Host = "127.0.0.1:30142"
			req.Header.Set("Authorization", "Bearer "+testToken)
			if accept != "" {
				req.Header.Set("Accept-Encoding", accept)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 %d", rec.Code)
			}
			if rec.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatal("缺少 Vary: Accept-Encoding")
			}
			declared := rec.Header().Get("Content-Encoding")
			body := rec.Body.Bytes()
			// 先按 identity 取一份原文，确认样本真的超过压缩阈值，
			// 否则后面的编码断言可能一条都没跑到。
			if raw := plainBody(t, s, "/ui/sessions"); len(raw) < 1024 {
				t.Fatalf("样本仅 %d 字节，不足以验证压缩路径", len(raw))
			}
			switch declared {
			case "":
				if !bytes.Contains(body, []byte("session-item")) {
					t.Fatal("未压缩响应不是预期 HTML")
				}
			case "gzip":
				zr, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatalf("gzip 流无法解析: %v", err)
				}
				plain, err := io.ReadAll(zr)
				if err != nil {
					t.Fatalf("gzip 解压失败: %v", err)
				}
				if !bytes.Contains(plain, []byte("session-item")) {
					t.Fatal("解压后不是预期 HTML")
				}
			case "br":
				plain, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
				if err != nil {
					t.Fatalf("brotli 解压失败: %v", err)
				}
				if !bytes.Contains(plain, []byte("session-item")) {
					t.Fatal("解压后不是预期 HTML")
				}
				// 用 gzip 解析必须失败，否则说明选错了压缩器。
				if _, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
					t.Fatal("br 流被 gzip 解析成功，压缩器选择错误")
				}
			default:
				t.Fatalf("未知编码 %q", declared)
			}
		})
	}
}

// plainBody 取一次不协商编码的响应原文，用于确认样本规模。
func plainBody(t *testing.T, s *Server, path string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Body.Bytes()
}

// Test导出下载受鉴权与路径约束 覆盖三点：
// 未授权拿不到、路径穿越拿不到、合法文件名能拿到且带下载头。
func Test导出下载受鉴权与路径约束(t *testing.T) {
	s, _, _ := newTestServer(t)
	if err := os.MkdirAll(s.exportDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.exportDir, "ok.html"), []byte("<html>导出内容</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	// 放一个目录外的文件，确认穿越取不到。
	outside := filepath.Join(filepath.Dir(s.exportDir), "secret.html")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	get := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1:30142"
		if auth {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/ui/exports/ok.html", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未授权应 401，实际 %d", rec.Code)
	}
	for _, bad := range []string{
		"/ui/exports/../secret.html",
		"/ui/exports/..%2fsecret.html",
		"/ui/exports/none.html",
		"/ui/exports/",
	} {
		if rec := get(bad, true); rec.Code == http.StatusOK {
			t.Errorf("%s 不应成功", bad)
		}
	}
	rec := get("/ui/exports/ok.html", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法导出应 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "导出内容") {
		t.Fatal("响应不是预期内容")
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "ok.html") {
		t.Fatalf("缺少下载头: %q", disposition)
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("缺少 Vary: Accept-Encoding")
	}
}

// TestState透传队列与自动压缩 确认 Pi 可读回的三个字段真的到了 State。
// 这三个字段此前的 State 投影里没有，前端无法反映当前排队模式与自动压缩。
func TestState透传队列与自动压缩(t *testing.T) {
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
	send(map[string]any{"version": 1, "kind": "command", "requestId": "s1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	if started["ok"] != true {
		t.Fatalf("启动失败: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	send(map[string]any{"version": 1, "kind": "command", "requestId": "s2", "sessionId": sessionID, "method": "session.state"})
	m := read()
	if m["ok"] != true {
		t.Fatalf("读取状态失败: %v", m)
	}
	raw, _ := json.Marshal(m["data"])
	for _, field := range []string{"steeringMode", "followUpMode", "autoCompactionEnabled"} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("状态缺少 %s: %s", field, raw)
		}
	}
}

// Test小片段不标ContentEncoding 是回归测试。
//
// writeHTML 曾经只要协商到编码就设 Content-Encoding，不看 ShouldCompress。
// 于是任何小于 1 KB 的 HTML 片段被标成 br，写的却是明文——浏览器按 br
// 解压明文必然失败，fetch 直接 reject（"Failed to fetch"），htmx 换不进去。
// 影响面：历史分页的小页、扩展对话框、包清单，以及一切短片段。
//
// 现有 Test协商编码与实际压缩器一致 只造了超过阈值的样本，覆盖不到这条。
func Test小片段不标ContentEncoding(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/templates/sessions.html", `{{range .Items}}<a class="session-item" href="/?session={{.ID}}" data-session="{{.ID}}"><span class="session-title">{{.Title}}</span></a>{{end}}`)
	write("ui-manifest.json", `{"protocolVersion":1,"requiredMethods":[],"templates":{"sessions":"templates/sessions.html"},"build":{"entry":"src/entry/app.ts"}}`)
	write("dist/.vite/manifest.json", `{"src/entry/app.ts":{"file":"assets/app-abc123.js","isEntry":true,"css":[]}}`)
	write("dist/assets/app-abc123.js", "console.log(1)")
	t.Setenv("PI_WEBUI_DIR", dir)
	s, _, cwd := newTestServer(t)
	// 只放一个会话，让片段远小于压缩阈值。
	writeSessionFile(t, s.store.Dir(), "sess-small", cwd)

	req := httptest.NewRequest(http.MethodGet, "/ui/sessions", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	body := rec.Body.Bytes()
	if len(body) >= 1024 {
		t.Fatalf("样本 %d 字节，没落在阈值以下，测不到这条路径", len(body))
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("小于阈值的响应不得标 Content-Encoding（实际 %q），客户端会按该编码解压明文", enc)
	}
	if !bytes.Contains(body, []byte("session-item")) {
		t.Fatal("未压缩响应不是预期 HTML")
	}
}

// Test静态资产始终声明Vary 覆盖 B61：identity 响应以前不带 Vary，
// 共享缓存可能把 brotli 变体回给不支持的客户端。
// 同时覆盖 B60：br;q=0 时不得仍选 br。
func Test静态资产始终声明Vary(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/templates/shell.html", `<html><body>{{.SessionID}}</body></html>`)
	write("ui-manifest.json", `{"protocolVersion":1,"requiredMethods":[],"templates":{"shell":"templates/shell.html"},"build":{"entry":"src/entry/app.ts"}}`)
	write("dist/.vite/manifest.json", `{"src/entry/app.ts":{"file":"assets/app-abc123.js","isEntry":true,"css":[]}}`)
	write("dist/assets/app-abc123.js", "console.log(1)")
	t.Setenv("PI_WEBUI_DIR", dir)
	s, _, _ := newTestServer(t)

	cases := []struct{ accept, wantEncoding string }{
		{"", ""},
		{"identity", ""},
		{"br", "br"},
		{"gzip", "gzip"},
		{"br, gzip", "br"},
		{"br;q=0, gzip;q=1", "gzip"},
		{"br;q=0", ""},
		{"*", "br"},
	}
	for _, tc := range cases {
		name := tc.accept
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
			req.Host = "127.0.0.1:30142"
			req.Header.Set("Authorization", "Bearer "+testToken)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 %d", rec.Code)
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Fatalf("Accept-Encoding %q 缺少 Vary: %q", tc.accept, got)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tc.wantEncoding {
				t.Fatalf("Accept-Encoding %q -> %q，期望 %q", tc.accept, got, tc.wantEncoding)
			}
			body := rec.Body.Bytes()
			switch tc.wantEncoding {
			case "":
				if string(body) != "console.log(1)" {
					t.Fatalf("identity 响应应保持原文: %q", body)
				}
			case "gzip":
				zr, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				plain, err := io.ReadAll(zr)
				if err != nil || string(plain) != "console.log(1)" {
					t.Fatalf("gzip 解压结果不符: %q %v", plain, err)
				}
			case "br":
				out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
				if err != nil || string(out) != "console.log(1)" {
					t.Fatalf("brotli 解压结果不符: %q %v", out, err)
				}
			}
		})
	}
}

// replyCode 从响应帧里取协议错误码，供测试断言。
func replyCode(m map[string]any) string {
	errObj, _ := m["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

// TestB07大文件读取不断线 覆盖 B07：
// 桥接受 600 KiB 文件读取，但 JSON 响应超过 WS 帧上限后旧实现直接取消连接，
// 常规文件预览会让整个会话断线。这里验证读取与连接都保持可用。
func TestB07大文件读取不断线(t *testing.T) {
	// newTestServer 的工作区根就是它自己那个 cwd，必须用同一个。
	s, _, cwd := newTestServer(t)
	big := strings.Repeat("a", 600<<10)
	if err := os.WriteFile(filepath.Join(cwd, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
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
	// 客户端默认 32 KiB 读限，这里要收的是被桥截断后的整段文本。
	conn.SetReadLimit(1 << 20)
	send := func(id, method string, params map[string]any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"version": 1, "kind": "command", "requestId": id, "method": method, "params": params})
		if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取响应失败（连接可能已被取消）: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	m := send("r1", "files.read", map[string]any{"path": filepath.Join(cwd, "big.txt")})
	if m["ok"] != true {
		t.Fatalf("大文件读取应成功: %v", m)
	}
	// 必须显式标记截断：调用方需要知道这不是完整内容。
	data, _ := m["data"].(map[string]any)
	if data["truncated"] != true {
		t.Fatalf("超出 WS 预算时应标记 truncated: %v", data)
	}
	// 连接必须仍可用。
	if m := send("r2", "worker.list", map[string]any{}); m["ok"] != true {
		t.Fatalf("大文件读取后连接不可用: %v", m)
	}
}

// TestHTTP文件文本端点返回完整内容 覆盖 B07 的另一半：
// WS 只给截断预览，完整内容必须能经 HTTP 取到，否则大文件永远看不全。
func TestHTTP文件文本端点返回完整内容(t *testing.T) {
	s, _, cwd := newTestServer(t)
	content := strings.Repeat("b", 600<<10)
	if err := os.WriteFile(filepath.Join(cwd, "full.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	req := httptest.NewRequest(http.MethodGet, "/ui/file-text?path="+url.QueryEscape(filepath.Join(cwd, "full.txt")), nil)
	req.Host = s.host
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Truncated"); got != "" {
		t.Fatalf("未超预算不应标记截断: %q", got)
	}
	if rec.Body.Len() != len(content) {
		t.Fatalf("HTTP 端点应返回完整内容: %d != %d", rec.Body.Len(), len(content))
	}
	// 不支持的路径仍要被拒绝，不能借这个端点绕过沙箱。
	bad := httptest.NewRequest(http.MethodGet, "/ui/file-text?path="+url.QueryEscape("/etc/passwd"), nil)
	bad.Host = s.host
	bad.Header.Set("Authorization", "Bearer "+testToken)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, bad)
	if rec2.Code == http.StatusOK {
		t.Fatal("越界路径不应通过 HTTP 端点读取")
	}
}

// TestWS读上限覆盖附件体积 覆盖 U05：
// 读上限以前是 1 MiB，而图片附件的合法体积约 96 MiB——
// 稍大的图片不仅发不出去，超限帧还会直接断开连接。
func TestWS读上限覆盖附件体积(t *testing.T) {
	want := pi.MaxImages*pi.MaxImageDataLen + (1 << 20)
	if wsReadLimit != want {
		t.Fatalf("WS 读上限未与附件预算对齐: %d != %d", wsReadLimit, want)
	}
	if wsReadLimit <= 1<<20 {
		t.Fatalf("WS 读上限仍低于合法附件体积: %d", wsReadLimit)
	}
	// 单张上限乘以张数必须能装进读上限，否则满额附件永远发不出。
	if pi.MaxImages*pi.MaxImageDataLen >= wsReadLimit {
		t.Fatal("满额附件会超出 WS 读上限")
	}
}

// TestWS实际读上限与预算一致 覆盖 U05 的调用点：
// 只校验常量不够——反例把 SetReadLimit 改回 1 MiB 时常量断言照样通过，
// 因为那一行根本没被读到。这里用真实连接发送接近预算的帧来验证。
func TestWS实际读上限与预算一致(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// 发一个超过旧 1 MiB 上限、但在附件预算之内的帧：
	// 旧读限会让服务端直接断开连接，这里必须仍能收到响应。
	big := strings.Repeat("a", 4<<20)
	body, _ := json.Marshal(map[string]any{
		"version": 1, "kind": "command", "requestId": "big-1", "method": "files.read",
		"params": map[string]any{"path": big},
	})
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("发送大帧失败（读上限可能仍过低）: %v", err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("大帧之后连接不可用: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["kind"] != "response" {
		t.Fatalf("没有收到响应: %v", m)
	}
}

// 204 分支按错误码判定，因此「什么错误码」必须稳：
// 损坏的历史文件是 invalid_history，绝不能落进「分支尚未落盘」那条路——
// 那会让真实的数据损坏在界面上表现成「稍后会出现的空分支」。
func Test损坏历史不得被当作未落盘分支(t *testing.T) {
	requireUI(t)
	s, manager, cwd := newTestServer(t)
	worker, err := manager.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	id := worker.Info().SessionID
	path := filepath.Join(s.store.Dir(), id+".jsonl")
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/sessions/"+id+"/history", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("损坏的历史记录不得返回 204（会被读成未落盘分支）: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Session-Unsaved") != "" {
		t.Fatal("损坏历史不得带未落盘标记")
	}
}

// 片段提示只显示最外层的中文提示；原因可能含本机路径或上游原文，不能露出去。
func Test片段提示不泄露错误原因(t *testing.T) {
	requireUI(t)
	s, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.fragmentIssue(rec, "", protocol.Wrap("pi_error", "请求供应商失败",
		errors.New("dial tcp /home/operator/.config/pi/agent/auth.json: connection refused")))
	body := rec.Body.String()
	if !strings.Contains(body, "请求供应商失败") {
		t.Fatalf("片段应展示外层提示: %s", body)
	}
	if strings.Contains(body, "auth.json") || strings.Contains(body, "dial tcp") {
		t.Fatalf("片段不得带出底层原因: %s", body)
	}
}

// testOptions 给出合法的构造参数，供校验用例改单个字段后使用。
func testOptions(t *testing.T) Options {
	t.Helper()
	srv, m, cwd := newTestServer(t)
	return Options{
		Manager: m, Store: srv.store, Terminals: srv.terminals, Files: srv.files,
		Config: srv.piConfig, Discovery: srv.discovery, ExportDir: srv.exportDir,
		Receipts: srv.receipts, Metrics: srv.metrics, Token: testToken,
		Host: "127.0.0.1:30142", UI: srv.ui, WorkspaceRoot: cwd,
	}
}
