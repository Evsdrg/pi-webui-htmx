package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readModelsFile(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		t.Fatal(err)
	}
	return out
}

func TestWriteModels原子写入(t *testing.T) {
	dir := t.TempDir()
	c := NewConfig(dir, DefaultLimits())
	doc := map[string]any{"providers": map[string]any{
		// models 是数组，与 Pi 的 ProviderConfigSchema 一致。
		"p1": map[string]any{"api": "https://example.com/v1", "models": []any{map[string]any{"id": "m1", "name": "模型一"}}},
	}}
	if err := c.WriteModels(doc); err != nil {
		t.Fatal(err)
	}
	got := readModelsFile(t, dir)
	providers, _ := got["providers"].(map[string]any)
	if len(providers) != 1 {
		t.Fatalf("写入内容异常: %v", got)
	}
	// 临时文件必须已被替换掉。
	if _, err := os.Stat(filepath.Join(dir, "models.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("临时文件应已被替换")
	}
	raw, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["providers"].(map[string]any); !ok {
		t.Fatalf("读回异常: %v", raw)
	}
}

func TestWriteModels拒绝损坏配置(t *testing.T) {
	dir := t.TempDir()
	c := NewConfig(dir, DefaultLimits())
	cases := map[string]map[string]any{
		"缺少 providers":  {"nope": 1},
		"providers 非对象": {"providers": []any{1, 2}},
		"provider 非对象":  {"providers": map[string]any{"p": "字符串"}},
		// Pi 的 api 只是非空字符串，不要求 http(s)；桥不额外收紧。
		"api 为空":     {"providers": map[string]any{"p": map[string]any{"api": ""}}},
		"模型缺 id":     {"providers": map[string]any{"p": map[string]any{"models": []any{map[string]any{"name": "无 id"}}}}},
		"models 非数组": {"providers": map[string]any{"p": map[string]any{"models": map[string]any{"m": map[string]any{}}}}},
		"模型条目非对象":    {"providers": map[string]any{"p": map[string]any{"models": []any{"字符串"}}}},
	}
	for name, doc := range cases {
		if err := c.WriteModels(doc); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "models.json")); !os.IsNotExist(err) {
		t.Fatal("被拒绝的写入不得落盘")
	}
}

func TestWriteModels保留密钥字段(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"),
		[]byte(`{"providers":{"p":{"apiKey":"sk-real"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewConfig(dir, DefaultLimits())
	raw, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	p := raw["providers"].(map[string]any)["p"].(map[string]any)
	if p["apiKey"] != "***" {
		t.Fatalf("读取必须打码: %v", p["apiKey"])
	}
	if err := c.WriteModels(map[string]any{"providers": map[string]any{"p": map[string]any{"apiKey": "sk-real"}}}); err != nil {
		t.Fatal(err)
	}
	pp := readModelsFile(t, dir)["providers"].(map[string]any)["p"].(map[string]any)
	if pp["apiKey"] != "sk-real" {
		t.Fatalf("写回应保留密钥: %v", pp["apiKey"])
	}
}

func TestWriteModels体积上限(t *testing.T) {
	dir := t.TempDir()
	c := NewConfig(dir, Limits{MaxFileBytes: 256, MaxModels: 512})
	doc := map[string]any{"providers": map[string]any{
		"p": map[string]any{"models": map[string]any{"m": map[string]any{"name": string(make([]byte, 4096))}}},
	}}
	if err := c.WriteModels(doc); err == nil {
		t.Fatal("超过体积上限必须被拒绝")
	}
}
