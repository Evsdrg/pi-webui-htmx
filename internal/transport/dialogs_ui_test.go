package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func Test对话框端点对话不存在返回204(t *testing.T) {
	if os.Getenv("PI_WEBUI_DIR") == "" {
		t.Skip("需要 PI_WEBUI_DIR")
	}
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/extensions/dialog/nope", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("对话不存在应 204，实际 %d", rec.Code)
	}
}

func Test对话框端点拒绝非法ID(t *testing.T) {
	if os.Getenv("PI_WEBUI_DIR") == "" {
		t.Skip("需要 PI_WEBUI_DIR")
	}
	s, _, _ := newTestServer(t)
	for _, bad := range []string{"/ui/extensions/dialog/", "/ui/extensions/dialog/a/b"} {
		req := httptest.NewRequest(http.MethodGet, bad, nil)
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q 应 400，实际 %d", bad, rec.Code)
		}
	}
}

func Test回执端点校验(t *testing.T) {
	if os.Getenv("PI_WEBUI_DIR") == "" {
		t.Skip("需要 PI_WEBUI_DIR")
	}
	s, _, _ := newTestServer(t)
	cases := []struct {
		name string
		body string
		ct   string
	}{
		{"缺 id", "value=x", "application/x-www-form-urlencoded"},
		{"非法 sessionId", url.Values{"id": {"d1"}, "value": {"x"}}.Encode(), "application/x-www-form-urlencoded"},
		{"坏 JSON", `{"id":`, "application/json"},
	}
	for _, c := range cases {
		path := "/ui/sessions/!!bad!!/ui-response"
		if c.name == "缺 id" {
			path = "/ui/sessions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/ui-response"
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body))
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", c.ct)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code == http.StatusNoContent {
			t.Fatalf("%s 不应成功", c.name)
		}
	}
}

func Test回执端点没有活跃worker时报错(t *testing.T) {
	if os.Getenv("PI_WEBUI_DIR") == "" {
		t.Skip("需要 PI_WEBUI_DIR")
	}
	s, _, _ := newTestServer(t)
	form := url.Values{"id": {"d1"}, "value": {"choice-a"}}.Encode()
	req := httptest.NewRequest(http.MethodPost,
		"/ui/sessions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/ui-response",
		strings.NewReader(form))
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无活跃 worker 应 400，实际 %d", rec.Code)
	}
	var got map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatal(rec.Body.String())
	}
	errObj, _ := got["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "worker_not_running" {
		t.Fatalf("错误码应为 worker_not_running，实际 %v", errObj)
	}
}

func Test扩展状态快照有界且按key排序(t *testing.T) {
	st := newExtensionState(4)
	st.update("mc", "mc: 12 (3%) · idle")
	st.update("aaa", "aaa: x")
	st.update("bbb", "")
	snap := st.snapshot()
	if len(snap) != 2 {
		t.Fatalf("应有 2 条（bbb 被清除），实际 %d", len(snap))
	}
	if snap[0].key != "aaa" || snap[1].key != "mc" {
		t.Fatalf("应按 key 排序: %+v", snap)
	}
	// 超过上限时淘汰，不无界增长。
	for i := 0; i < 20; i++ {
		st.update(string(rune('A'+i)), "v")
	}
	if len(st.snapshot()) > 4 {
		t.Fatalf("应受上限约束，实际 %d", len(st.snapshot()))
	}
}

func TestParseSetStatus只认setStatus(t *testing.T) {
	cases := []struct {
		body string
		ok   bool
	}{
		{`{"type":"extension_ui_request","method":"setStatus","statusKey":"mc","statusText":"idle"}`, true},
		{`{"type":"extension_ui_request","method":"select","title":"选一个"}`, false},
		{`{"type":"extension_ui_request","method":"notify","message":"hi"}`, false},
		{`{"type":"message_end"}`, false},
		{`坏 JSON`, false},
	}
	for _, c := range cases {
		_, _, ok := parseSetStatus([]byte(c.body))
		if ok != c.ok {
			t.Fatalf("%s -> %v，期望 %v", c.body, ok, c.ok)
		}
	}
}
