package management

import (
	"os"
	"path/filepath"
	"testing"
)

// 启动守卫依赖「这个 agent 目录里到底配没配插件」这个只读判断：
// 自动加载的扩展文件（extensions/*.ts|js 或目录入口）与 settings.json 的
// packages 任意非空即视为「配了插件」。它必须只读本地文件、不发网络请求，
// 也不能因 settings.json 缺失或损坏而报错——守卫只关心「有没有」。

func TestConfiguredPlugins识别扩展文件(t *testing.T) {
	dir := t.TempDir()
	mkdirExt(t, dir, "zh-system-prompt.ts")
	c := NewConfig(dir, DefaultLimits())
	extensions, packages := c.ConfiguredPlugins()
	if extensions != 1 || packages != 0 {
		t.Fatalf("应识别 1 个扩展、0 个包，实际 %d/%d", extensions, packages)
	}
}

func TestConfiguredPlugins识别packages(t *testing.T) {
	dir := t.TempDir()
	settings := `{"packages":["npm:pi-goal-x@0.31.9"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewConfig(dir, DefaultLimits())
	extensions, packages := c.ConfiguredPlugins()
	if extensions != 0 || packages != 1 {
		t.Fatalf("应识别 0 个扩展、1 个包，实际 %d/%d", extensions, packages)
	}
}

func TestConfiguredPlugins空目录不计(t *testing.T) {
	dir := t.TempDir()
	c := NewConfig(dir, DefaultLimits())
	if extensions, packages := c.ConfiguredPlugins(); extensions != 0 || packages != 0 {
		t.Fatalf("空目录不应计任何插件，实际 %d/%d", extensions, packages)
	}
}

func TestConfiguredPlugins坏settings不报错(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{ 不是 JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewConfig(dir, DefaultLimits())
	// 坏文件按「读不出、当 0」处理，绝不能 panic 或阻塞启动。
	if extensions, packages := c.ConfiguredPlugins(); extensions != 0 || packages != 0 {
		t.Fatalf("坏 settings 应视为无包，实际 %d/%d", extensions, packages)
	}
}
