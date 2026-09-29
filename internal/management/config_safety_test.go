package management

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func Test配置秘密按模型身份保留(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"p":{"apiKey":"!existing-local-helper","headers":{"x-api-key":"secret-provider","Content-Type":"application/json"},"models":[{"id":"a","headers":{"Authorization":"secret-a"}},{"id":"b","headers":{"X-Custom":"secret-b"}}],"modelOverrides":{"builtin":{"headers":{"X-Hidden":"secret-override"}}}}}}`)
	c := NewConfig(dir, DefaultLimits())
	doc, err := c.Raw()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(doc)
	if bytes.Contains(encoded, []byte("secret-")) || bytes.Contains(encoded, []byte("existing-local-helper")) {
		t.Fatal("读取泄露了凭据或本机表达式")
	}
	p := doc["providers"].(map[string]any)["p"].(map[string]any)
	models := p["models"].([]any)
	models[0], models[1] = models[1], models[0]
	models[0].(map[string]any)["name"] = "重排后修改名称"
	before, _ := json.Marshal(doc)
	if err := c.WriteModels(doc); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(doc)
	if !bytes.Equal(before, after) {
		t.Fatal("保存修改了调用方的脱敏对象")
	}
	saved := readModelsFile(t, dir)["providers"].(map[string]any)["p"].(map[string]any)
	list := saved["models"].([]any)
	if list[0].(map[string]any)["headers"].(map[string]any)["X-Custom"] != "secret-b" || list[1].(map[string]any)["headers"].(map[string]any)["Authorization"] != "secret-a" {
		t.Fatal("模型重排后凭据未按 ID 保留")
	}
	if saved["apiKey"] != "!existing-local-helper" || saved["headers"].(map[string]any)["Content-Type"] != "application/json" {
		t.Fatal("本机表达式或普通头部未保留")
	}
	if saved["modelOverrides"].(map[string]any)["builtin"].(map[string]any)["headers"].(map[string]any)["X-Hidden"] != "secret-override" {
		t.Fatal("modelOverrides 的凭据丢失")
	}
}

func Test提供商名称不按秘密字段脱敏(t *testing.T) {
	for _, name := range []string{"token", "headers", "apiKey"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, "models.json", `{"providers":{"`+name+`":{"apiKey":"secret"}}}`)
			c := NewConfig(dir, DefaultLimits())
			doc, err := c.Raw()
			if err != nil {
				t.Fatal(err)
			}
			p, ok := doc["providers"].(map[string]any)[name].(map[string]any)
			if !ok || p["apiKey"] != "***" {
				t.Fatal("提供商名称被误当成配置字段")
			}
			if err := c.WriteModels(doc); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func Test禁止新增执行表达式或移动占位符(t *testing.T) {
	for name, input := range map[string]string{
		"新增命令":   `{"providers":{"p":{"apiKey":"!new-helper"}}}`,
		"修改命令":   `{"providers":{"old":{"apiKey":"!changed-helper"}}}`,
		"模型头部命令": `{"providers":{"p":{"models":[{"id":"a","headers":{"X-Key":"!new-helper"}}]}}}`,
		"覆盖模型命令": `{"providers":{"p":{"modelOverrides":{"a":{"headers":{"X-Key":"!new-helper"}}}}}}`,
		"移动占位符":  `{"providers":{"new":{"apiKey":"***"}}}`,
		"重复模型身份": `{"providers":{"p":{"models":[{"id":"a"},{"id":"a"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			const original = `{"providers":{"old":{"apiKey":"!existing-helper"}}}`
			writeConfig(t, dir, "models.json", original)
			var doc map[string]any
			if err := json.Unmarshal([]byte(input), &doc); err != nil {
				t.Fatal(err)
			}
			if err := NewConfig(dir, DefaultLimits()).WriteModels(doc); err == nil {
				t.Fatal("不明确或新增执行型配置被接受")
			}
			got, err := os.ReadFile(filepath.Join(dir, "models.json"))
			if err != nil || string(got) != original {
				t.Fatal("拒绝的写入破坏了原配置")
			}
		})
	}
}

func Test临时配置链接不能覆盖其他文件(t *testing.T) {
	dir := t.TempDir()
	guard := filepath.Join(t.TempDir(), "guard")
	if err := os.WriteFile(guard, []byte("保持不变"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(guard, filepath.Join(dir, "models.json.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := NewConfig(dir, DefaultLimits()).WriteModels(map[string]any{"providers": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(guard)
	if err != nil || string(got) != "保持不变" {
		t.Fatal("固定临时链接的目标被覆写")
	}
	info, err := os.Lstat(filepath.Join(dir, "models.json"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("配置文件类型/权限错误：%v %v", info, err)
	}
}

func Test模型摘要按真实数组限制总数量(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "models.json", `{"providers":{"a":{"models":[{"id":"a1"},{"id":"a2"}]},"b":{"models":[{"id":"b1"},{"id":"b2"}]}}}`)
	limits := DefaultLimits()
	limits.MaxModels = 3
	out, err := NewConfig(dir, limits).Models()
	if err != nil {
		t.Fatal(err)
	}
	if out.ModelCount != 4 {
		t.Fatalf("原始模型数错误：%v", out.ModelCount)
	}
	providers := out.Providers
	count := 0
	for _, v := range providers {
		count += len(v.(map[string]any)["models"].([]any))
	}
	if count != 3 || providers["b"].(map[string]any)["truncated"] != true {
		t.Fatal("模型总数未受限或未标记截断")
	}
}
