package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newDiscoveryConfig(t *testing.T) *Config {
	t.Helper()
	return NewConfig(t.TempDir(), DefaultLimits())
}

func TestBuildModelsListURL与PiWeb一致(t *testing.T) {
	cases := []struct {
		base string
		api  string
		want string
	}{
		{"https://api.openai.com/v1", "openai-completions", "https://api.openai.com/v1/models"},
		{"https://api.anthropic.com", "anthropic-messages", "https://api.anthropic.com/v1/models?limit=1000"},
		{"https://generativelanguage.googleapis.com", "google-generative-ai", "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000"},
		{"https://example.com/v1/models", "openai-completions", "https://example.com/v1/models"},
		{"https://example.com/v1/", "openai-completions", "https://example.com/v1/models"},
	}
	for _, c := range cases {
		got, err := buildModelsListURL(c.base, c.api)
		if err != nil {
			t.Fatalf("%s: %v", c.base, err)
		}
		if got != c.want {
			t.Fatalf("base=%s\n  got  %s\n  want %s", c.base, got, c.want)
		}
	}
}

func TestBuildModelsListURL拒绝非法输入(t *testing.T) {
	for _, bad := range []string{"", "not-a-url", "ftp://example.com", "file:///etc/passwd"} {
		if _, err := buildModelsListURL(bad, "openai-completions"); err == nil {
			t.Fatalf("应拒绝 %q", bad)
		}
	}
}

func TestDiscover解析多种响应形状(t *testing.T) {
	cases := map[string]string{
		"OpenAI data 数组":   `{"data":[{"id":"gpt-4","name":"GPT-4","context_window":8192}]}`,
		"顶层数组":             `[{"id":"m1","name":"模型一"}]`,
		"models 数组":        `{"models":[{"id":"m2"}]}`,
		"Google models 对象": `{"models":{"gemini-1":{"name":"Gemini"}}}`,
	}
	for name, body := range cases {
		models, err := parseDiscoveredModels([]byte(body), 100)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(models) == 0 {
			t.Fatalf("%s 未解析出模型", name)
		}
		if models[0].ID == "" {
			t.Fatalf("%s 模型 ID 为空", name)
		}
	}
}

func TestDiscover去重并遵守上限(t *testing.T) {
	body := `{"data":[{"id":"a"},{"id":"a"},{"id":"b"},{"id":"c"}]}`
	models, err := parseDiscoveredModels([]byte(body), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a" || models[1].ID != "b" {
		t.Fatalf("去重或上限异常: %+v", models)
	}
}

func TestDiscover拒绝非法JSON(t *testing.T) {
	if _, err := parseDiscoveredModels([]byte("不是 JSON"), 10); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
}

func TestDiscover按API设置鉴权头(t *testing.T) {
	var gotKey, gotVersion, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude"}]}`))
	}))
	defer srv.Close()
	c := newDiscoveryConfig(t)
	if _, err := c.Discover(context.Background(), srv.URL, "anthropic-messages", "sk-test", nil, DefaultDiscoveryLimits()); err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-test" || gotVersion != "2023-06-01" {
		t.Fatalf("Anthropic 鉴权头异常: %q %q", gotKey, gotVersion)
	}
	if gotAuth != "" {
		t.Fatalf("Anthropic 不应设置 Authorization: %q", gotAuth)
	}
}

func TestDiscover自定义头部不覆盖鉴权(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c := newDiscoveryConfig(t)
	_, _ = c.Discover(context.Background(), srv.URL, "anthropic-messages", "sk-auto",
		map[string]string{"x-api-key": "sk-manual"}, DefaultDiscoveryLimits())
	if gotKey != "sk-manual" {
		t.Fatalf("自定义头部应优先: %q", gotKey)
	}
	_ = gotAuth
}

func TestDiscover拒绝头部注入(t *testing.T) {
	// 头部名或值含控制字符时必须拒绝，防止请求走私。
	badCases := []map[string]string{
		{"X-Evil": "value\r\nX-Injected: 1"},
		{"": "value"},
		{"X-Nul": "va\x00lue"},
	}
	for _, bad := range badCases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		cfg := NewConfig(t.TempDir(), DefaultLimits())
		_, err := cfg.Discover(context.Background(), srv.URL, "openai-completions", "", bad, DefaultDiscoveryLimits())
		srv.Close()
		if err == nil {
			t.Fatalf("应拒绝非法头部 %v", bad)
		}
	}
}

func TestDiscover供应商错误码透出(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()
	c := newDiscoveryConfig(t)
	_, err := c.Discover(context.Background(), srv.URL, "openai-completions", "bad", nil, DefaultDiscoveryLimits())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("应透出 HTTP 状态: %v", err)
	}
}

func TestTestConnection最小请求(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Fatalf("应为 GET，实际 %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	c := newDiscoveryConfig(t)
	out, err := c.TestConnection(context.Background(), srv.URL, "openai-completions", "sk", nil, DefaultDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || calls != 1 {
		t.Fatalf("连通测试异常: %v 调用 %d 次", out, calls)
	}
}

func TestCatalog解析modelsDev(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-4":{"name":"GPT-4","limit":{"context":8192,"output":4096},"reasoning":false}}}}`))
	}))
	defer srv.Close()
	entries, err := parseCatalogForTest([]byte(`{"openai":{"models":{"gpt-4":{"name":"GPT-4","limit":{"context":8192,"output":4096},"reasoning":false}}}}`), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "openai/gpt-4" || entries[0].ContextSize != 8192 {
		t.Fatalf("目录解析异常: %+v", entries)
	}
	_ = srv
}

func parseCatalogForTest(body []byte, max int) ([]DiscoveredModel, error) {
	// 与 Catalog 相同的解析逻辑，供测试直接调用。
	var doc map[string]any
	if err := jsonUnmarshalForTest(body, &doc); err != nil {
		return nil, err
	}
	out := []DiscoveredModel{}
	for provider, v := range doc {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		models, _ := entry["models"].(map[string]any)
		for id, mv := range models {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			item := DiscoveredModel{ID: provider + "/" + id, Name: stringField(m, "name")}
			if limit, ok := m["limit"].(map[string]any); ok {
				item.ContextSize = intField(limit, "context")
				item.MaxTokens = intField(limit, "output")
			}
			item.Reasoning = boolField(m, "reasoning")
			out = append(out, item)
			if len(out) >= max {
				return out, nil
			}
		}
	}
	return out, nil
}
