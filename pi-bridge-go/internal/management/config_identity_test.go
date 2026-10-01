package management

import "testing"

func Test歧义秘密只能用明确新值修复(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"p":{"models":[{"id":"a","headers":{"X-Key":"one"}},{"id":"a","headers":{"X-Key":"two"}}]}}}`)
	c := NewConfig(dir, DefaultLimits())
	doc, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	p := doc["providers"].(map[string]any)["p"].(map[string]any)
	list := p["models"].([]any)
	p["models"] = list[:1]
	if err := c.WriteModels(doc); err == nil {
		t.Fatal("不能从重复 ID 猜测秘密")
	}
	list[0].(map[string]any)["headers"].(map[string]any)["X-Key"] = "replacement"
	if err := c.WriteModels(doc); err != nil {
		t.Fatalf("明确新值应允许修复歧义：%v", err)
	}
}

func Test头部大小写变化仍按同一身份保留(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"p":{"headers":{"Authorization":"secret"}}}}`)
	c := NewConfig(dir, DefaultLimits())
	doc, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	headers := doc["providers"].(map[string]any)["p"].(map[string]any)["headers"].(map[string]any)
	headers["authorization"] = headers["Authorization"]
	delete(headers, "Authorization")
	if err := c.WriteModels(doc); err != nil {
		t.Fatal(err)
	}
	value := readModelsFile(t, dir)["providers"].(map[string]any)["p"].(map[string]any)["headers"].(map[string]any)["authorization"]
	if value != "secret" {
		t.Fatal("头部大小写变化丢失秘密")
	}
}

func Test空对象与转义感叹号正确处理(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `null`)
	c := NewConfig(dir, DefaultLimits())
	if _, err := c.Raw(); err == nil {
		t.Fatal("null 不是配置对象")
	}
	if err := c.WriteModels(map[string]any{"providers": map[string]any{"p": map[string]any{"apiKey": "$!literal"}}}); err != nil {
		t.Fatalf("Pi 的感叹号转义不应误判为命令：%v", err)
	}
}
