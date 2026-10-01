package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func Test对话框端点对话不存在返回204(t *testing.T) {
	requireUI(t)
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
	requireUI(t)
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
	requireUI(t)
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
	requireUI(t)
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
