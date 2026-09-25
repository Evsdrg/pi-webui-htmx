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
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
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
	cfg := run.Defaults()
	cfg.Binary = fakePi
	cfg.AgentDir = agentDir
	cfg.Store = store
	cfg.Policy = policy
	cfg.MaxWorkers = 1
	cfg.IdleTimeout = 5 * time.Minute
	cfg.OperationTimeout = 2 * time.Second
	m := run.New(cfg)
	t.Cleanup(m.Close)
	return New(m, store, testToken, "127.0.0.1:30142"), m, cwd
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
	// 重复 requestId 必须拒绝，防止有副作用的命令被重放。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "r2", "method": "session.stop"})
	if m := read(); m["ok"] != false {
		t.Fatalf("重复 requestId 应被拒绝: %v", m)
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
	for _, method := range []string{"session.compact", "session.fork", "session.switch", "model.set"} {
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
