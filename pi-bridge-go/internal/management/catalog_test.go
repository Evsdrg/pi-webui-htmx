package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// B70：模型目录按 models.dev 的形状解析，并受 MaxCatalog 上限约束。
func TestCatalog解析models_dev形状(t *testing.T) {
	body := `{
	  "deepseek": {"models": {
	    "deepseek-chat": {"name": "DeepSeek Chat", "limit": {"context": 128000, "output": 8192}, "reasoning": false, "input": ["text"]},
	    "deepseek-reasoner": {"name": "DeepSeek Reasoner", "limit": {"context": 128000, "output": 65536}, "reasoning": true, "input": ["text", "image"]}
	  }},
	  "openai": {"models": {"gpt-x": {"name": "GPT X"}}}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newDiscoveryConfig(t)
	c.SetCatalogURL(srv.URL)
	items, err := c.Catalog(context.Background(), DefaultDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]DiscoveredModel{}
	for _, item := range items {
		byID[item.ID] = item
	}
	reasoner, ok := byID["deepseek/deepseek-reasoner"]
	if !ok {
		t.Fatalf("目录条目应按 provider/model 命名: %+v", items)
	}
	if reasoner.Name != "DeepSeek Reasoner" || reasoner.ContextSize != 128000 || reasoner.MaxTokens != 65536 || !reasoner.Reasoning {
		t.Fatalf("目录字段解析不完整: %+v", reasoner)
	}
	if len(reasoner.Input) != 2 || reasoner.Input[1] != "image" {
		t.Fatalf("input 能力应保留: %+v", reasoner.Input)
	}
}

// 目录是联网数据：非法地址必须在配置阶段被忽略，而不是让出站请求打到别处。
func TestCatalogURL只接受http地址(t *testing.T) {
	c := newDiscoveryConfig(t)
	c.SetCatalogURL("file:///etc/passwd")
	if c.catalogURL != defaultCatalogURL {
		t.Fatalf("非 http(s) 来源应被忽略: %q", c.catalogURL)
	}
	c.SetCatalogURL("https://mirror.example/api.json")
	if c.catalogURL != "https://mirror.example/api.json" {
		t.Fatalf("合法镜像应生效: %q", c.catalogURL)
	}
	c.SetCatalogURL("   ")
	if c.catalogURL != "https://mirror.example/api.json" {
		t.Fatalf("空值不应清掉已有来源: %q", c.catalogURL)
	}
}

// 目录条目数量受上限约束：它来自公网，不能无界增长。
func TestCatalog受数量上限约束(t *testing.T) {
	var body []byte
	body = append(body, []byte(`{"p":{"models":{`)...)
	for i := 0; i < 50; i++ {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, []byte(`"m`+string(rune('a'+i%26))+string(rune('0'+i/26))+`":{"name":"x"}`)...)
	}
	body = append(body, []byte(`}}}`)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newDiscoveryConfig(t)
	c.SetCatalogURL(srv.URL)
	limits := DefaultDiscoveryLimits()
	limits.MaxCatalog = 10
	items, err := c.Catalog(context.Background(), limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) > 10 {
		t.Fatalf("目录条目应受 MaxCatalog 约束: %d", len(items))
	}
}
