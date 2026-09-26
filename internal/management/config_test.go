package management

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestModels密钥被打码(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{
	  "providers": {
	    "cpa": {
	      "api": "https://example.com/v1",
	      "apiKey": "sk-super-secret",
	      "models": {
	        "m1": {"name": "模型一", "contextWindow": 200000}
	      }
	    }
	  }
	}`)
	c := NewConfig(dir, DefaultLimits())
	out, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := out["providers"].(map[string]any)
	entry, _ := providers["cpa"].(map[string]any)
	if entry["apiKey"] != "***" {
		t.Fatalf("密钥必须被打码: %v", entry["apiKey"])
	}
	if entry["api"] != "https://example.com/v1" {
		t.Fatalf("非密钥字段不应被打码: %v", entry["api"])
	}
	if out["modelCount"].(int) != 1 {
		t.Fatalf("模型计数异常: %v", out["modelCount"])
	}
}

func TestModels不存在时返回空(t *testing.T) {
	c := NewConfig(t.TempDir(), DefaultLimits())
	out, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(out["providers"].(map[string]any)) != 0 {
		t.Fatalf("无配置时应返回空: %v", out)
	}
}

func TestModels非法JSON显式报错(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", "不是 JSON")
	c := NewConfig(dir, DefaultLimits())
	if _, err := c.Models(); err == nil {
		t.Fatal("非法 JSON 必须显式报错")
	}
}

func TestSettings打码与包名列表(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "settings.json", `{
	  "theme": "dark",
	  "packages": ["npm:@a/b", "npm:@c/d"],
	  "providers": {"openai": {"apiKey": "sk-x"}}
	}`)
	c := NewConfig(dir, DefaultLimits())
	out, err := c.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if out["theme"] != "dark" {
		t.Fatalf("普通字段不应受影响: %v", out["theme"])
	}
	packages, _ := out["packages"].([]string)
	if len(packages) != 2 || packages[0] != "npm:@a/b" {
		t.Fatalf("包名列表异常: %v", packages)
	}
	providers, _ := out["providers"].(map[string]any)
	openai, _ := providers["openai"].(map[string]any)
	if openai["apiKey"] != "***" {
		t.Fatalf("嵌套密钥必须被打码: %v", openai)
	}
}

func TestTrust只读(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "trust.json", `{"projects":{"/opt/x":{"trusted":true}}}`)
	c := NewConfig(dir, DefaultLimits())
	out, err := c.Trust()
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := out["projects"].(map[string]any)
	if len(projects) != 1 {
		t.Fatalf("信任记录异常: %v", out)
	}
}

func Test超长嵌套密钥同样打码(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"p":{"models":{"m":{"headers":{"Authorization":"Bearer sk-deep"}}}}}}`)
	c := NewConfig(dir, DefaultLimits())
	out, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	providers := out["providers"].(map[string]any)
	p := providers["p"].(map[string]any)
	models := p["models"].(map[string]any)
	m := models["m"].(map[string]any)
	headers := m["headers"].(map[string]any)
	if headers["Authorization"] != "***" {
		t.Fatalf("深层嵌套密钥必须被打码: %v", headers)
	}
}

func Test模型数量上限(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"providers":{"p":{"models":{`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"m` + string(rune('a'+i)) + `":{"name":"x"}`)
	}
	b.WriteString(`}}}}`)
	writeConfig(t, dir, "models.json", b.String())
	limits := DefaultLimits()
	limits.MaxModels = 5
	c := NewConfig(dir, limits)
	out, err := c.Models()
	if err != nil {
		t.Fatal(err)
	}
	p := out["providers"].(map[string]any)["p"].(map[string]any)
	if len(p["models"].(map[string]any)) != 5 {
		t.Fatalf("模型数应受上限约束: %v", p)
	}
	if p["truncated"] != true {
		t.Fatalf("应标记截断: %v", p)
	}
}

// Test回传占位符不覆写真实密钥 是回归测试。
// 曾经 WriteModels 直接序列化前端回传的文档，于是「取回 Raw() 改个模型名
// 再保存」会把全部 API key 写成字面量 "***"，之后所有请求都带着假密钥失败，
// 而界面照旧显示 "***"，看不出任何异常。
func Test回传占位符不覆写真实密钥(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{
	  "providers": {
	    "cpa": {
	      "api": "https://example.com/v1",
	      "apiKey": "sk-super-secret",
	      "models": [{"id": "m1", "name": "旧名字"}]
	    },
	    "other": {
	      "api": "https://other.example.com/v1",
	      "apiKey": "sk-other-secret"
	    }
	  }
	}`)
	c := NewConfig(dir, DefaultLimits())
	raw, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	// 前端只改模型显示名，密钥字段原样带回。
	providers, _ := raw["providers"].(map[string]any)
	entry, _ := providers["cpa"].(map[string]any)
	models, _ := entry["models"].([]any)
	m1, _ := models[0].(map[string]any)
	m1["name"] = "新名字"
	if err := c.WriteModels(raw); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "sk-super-secret") {
		t.Fatalf("真实密钥被占位符覆写: %s", body)
	}
	if !strings.Contains(string(body), "sk-other-secret") {
		t.Fatalf("未涉及的 provider 密钥丢失: %s", body)
	}
	if !strings.Contains(string(body), "新名字") {
		t.Fatalf("用户的修改没有落盘: %s", body)
	}
	if strings.Count(string(body), `"***"`) != 0 {
		t.Fatalf("磁盘上不应出现占位符: %s", body)
	}
}

// Test新provider的占位符按字面量写入 确认还原只针对磁盘上已有的真值，
// 不会把用户真的想写的 "***" 也吞掉。
func Test新provider的占位符按字面量写入(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"old":{"apiKey":"sk-old"}}}`)
	c := NewConfig(dir, DefaultLimits())
	if err := c.WriteModels(map[string]any{"providers": map[string]any{
		"old": map[string]any{"apiKey": "***"},
		"new": map[string]any{"apiKey": "***"},
	}}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if !strings.Contains(string(body), "sk-old") {
		t.Fatalf("已有密钥未还原: %s", body)
	}
	// 新增 provider 没有真值可还原，按用户提交的写。
	if strings.Count(string(body), `"***"`) != 1 {
		t.Fatalf("新 provider 的占位符应原样落盘: %s", body)
	}
}

// Test磁盘配置损坏时按用户提交写入 确认读取失败不阻塞保存。
func Test磁盘配置损坏时按用户提交写入(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{这不是合法 JSON`)
	c := NewConfig(dir, DefaultLimits())
	if err := c.WriteModels(map[string]any{"providers": map[string]any{
		"p": map[string]any{"apiKey": "sk-new"},
	}}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if !strings.Contains(string(body), "sk-new") {
		t.Fatalf("损坏磁盘不应阻塞写入: %s", body)
	}
}

// Test校验与Pi的schema一致 是回归测试。
// 桥曾经要求 provider.models 是对象、api 必须带 http(s) 前缀；
// Pi 的 ProviderConfigSchema 里 models 是数组、api 是任意非空字符串。
// 结果是一份 Pi 完全接受的配置被桥拒绝，用户改个模型名都保存不了。
func Test校验与Pi的schema一致(t *testing.T) {
	dir := t.TempDir()
	c := NewConfig(dir, DefaultLimits())
	// 这份配置与 Pi 自述 schema 一致，桥必须接受。
	valid := map[string]any{"providers": map[string]any{
		"fixture": map[string]any{
			"api":    "openai-completions", // 不是 URL，Pi 接受
			"apiKey": "sk-1",
			"models": []any{ // 数组，不是对象
				map[string]any{"id": "demo-fast", "name": "演示", "reasoning": true, "contextWindow": 128000},
				map[string]any{"id": "demo-review"},
			},
		},
	}}
	if err := c.WriteModels(valid); err != nil {
		t.Fatalf("符合 Pi schema 的配置被拒绝: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "models.json"))
	if !strings.Contains(string(body), "demo-review") {
		t.Fatalf("配置没有落盘: %s", body)
	}
	// 仍然要拦住真正损坏的：models 不是数组、缺 id。
	for name, bad := range map[string]map[string]any{
		"models 是对象":  {"providers": map[string]any{"p": map[string]any{"models": map[string]any{"a": map[string]any{}}}}},
		"模型缺 id":      {"providers": map[string]any{"p": map[string]any{"models": []any{map[string]any{"name": "无 id"}}}}},
		"provider 空名": {"providers": map[string]any{"": map[string]any{}}},
	} {
		if err := c.WriteModels(bad); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}
