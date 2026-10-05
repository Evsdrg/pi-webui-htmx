package management

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件覆盖「随 Pi 自动加载的扩展文件」清单。
//
// 背景：settings.json 的 packages 只覆盖 npm 包；<agent-dir>/extensions 下按目录
// 约定自动加载的 *.ts/*.js 扩展不在其中。只看 packages 会让用户误以为自己的
// 本地扩展没装上——这正是「汉化插件看不到」现象的根因。

func mkdirExt(t *testing.T, dir string, names ...string) {
	t.Helper()
	extDir := filepath.Join(dir, "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		path := filepath.Join(extDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("export default () => {}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// 单文件扩展（含 .ts 与 .js）都要列出；其它后缀与隐藏文件不算。
func TestExtensions列出单文件(t *testing.T) {
	dir := t.TempDir()
	mkdirExt(t, dir, "zh-system-prompt.ts", "helper.js", "readme.md", ".hidden.ts")
	c := NewConfig(dir, DefaultLimits())
	list, err := c.Extensions()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range list {
		got[e.Name] = e.Path
	}
	if got["zh-system-prompt.ts"] != filepath.Join("extensions", "zh-system-prompt.ts") {
		t.Fatalf("单文件 .ts 未列出: %+v", list)
	}
	if _, ok := got["helper.js"]; !ok {
		t.Fatalf("单文件 .js 未列出: %+v", list)
	}
	if _, ok := got["readme.md"]; ok {
		t.Fatalf("非扩展文件不应列出: %+v", list)
	}
	if _, ok := got[".hidden.ts"]; ok {
		t.Fatalf("隐藏文件不应列出: %+v", list)
	}
}

// 目录形态：含 index.ts/index.js，或含声明了 pi.extensions 的 package.json。
func TestExtensions列出目录入口(t *testing.T) {
	dir := t.TempDir()
	mkdirExt(t, dir,
		"pkg-a/index.ts",
		"pkg-b/package.json",
	)
	extDir := filepath.Join(dir, "extensions")
	if err := os.WriteFile(filepath.Join(extDir, "pkg-b", "package.json"),
		[]byte(`{"name":"pkg-b","pi":{"extensions":["main.js"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "pkg-b", "main.js"), []byte("export default () => {}"), 0600); err != nil {
		t.Fatal(err)
	}
	// 空目录不算扩展。
	if err := os.MkdirAll(filepath.Join(extDir, "empty-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	c := NewConfig(dir, DefaultLimits())
	list, err := c.Extensions()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range list {
		got[e.Name] = true
	}
	if !got["pkg-a"] || !got["pkg-b"] {
		t.Fatalf("目录入口未列出: %+v", list)
	}
	if got["empty-dir"] {
		t.Fatalf("空目录不应列出: %+v", list)
	}
}

// 没有 extensions 目录时返回空清单而不是报错（合法状态）。
func TestExtensions目录缺失返回空(t *testing.T) {
	c := NewConfig(t.TempDir(), DefaultLimits())
	list, err := c.Extensions()
	if err != nil {
		t.Fatalf("目录缺失不应报错: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("应为空清单: %+v", list)
	}
}
