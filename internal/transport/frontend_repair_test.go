package transport

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func Test发现模型片段必须认证且转义供应商内容(t *testing.T) {
	s := newTestServerWithUI(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"<script>alert(1)</script>","name":"测试"}]}`))
	}))
	defer upstream.Close()
	values := url.Values{"baseUrl": {upstream.URL}, "api": {"openai-completions"}, "apiKey": {"literal"}}
	for _, auth := range []bool{false, true} {
		req := httptest.NewRequest("POST", "/ui/models/discover", strings.NewReader(values.Encode()))
		req.Host = "127.0.0.1:30142"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if auth {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if !auth {
			if rec.Code != 401 {
				t.Fatal(rec.Code)
			}
			continue
		}
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "&lt;script&gt;") || strings.Contains(rec.Body.String(), "<script>") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
}

func Test记忆分页追加和完整正文路径(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	first := getFragment(t, s, "/ui/mc?kind=memories&limit=1")
	if !strings.Contains(first, "offset=1") {
		t.Fatal("缺少下一页")
	}
	last := getFragment(t, s, "/ui/mc?kind=memories&offset=2&limit=1&append=1")
	if !strings.Contains(last, `hx-swap-oob="beforeend:#mc-rows"`) || strings.Contains(last, "加载更多") || !strings.Contains(last, "id=3") {
		t.Fatal(last)
	}
	full := getFragment(t, s, "/ui/mc/content?kind=memories&id=3")
	if !strings.Contains(full, "MUST_NOT_APPEAR") {
		t.Fatal(full)
	}
	directives := getFragment(t, s, "/ui/mc/content?kind=directives&id=1")
	if !strings.Contains(directives, "用中文写注释") {
		t.Fatal(directives)
	}
}
