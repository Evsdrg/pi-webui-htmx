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
