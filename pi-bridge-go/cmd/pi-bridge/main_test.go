package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExt(t *testing.T, dir, name string) {
	t.Helper()
	extDir := filepath.Join(dir, "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, name), []byte("export default () => {}"), 0600); err != nil {
		t.Fatal(err)
	}
}

// 已配插件却没开扩展：必须拒绝启动，而不是静默不带插件跑。
func TestExtensionsGuard配了插件却未开扩展被拒(t *testing.T) {
	dir := t.TempDir()
	writeExt(t, dir, "zh-system-prompt.ts")
	if err := extensionsGuardError(dir, false, false); err == nil {
		t.Fatal("配了扩展却未开扩展时应报错")
	}
}

func TestExtensionsGuard配了包却未开扩展被拒(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"packages":["npm:pi-goal-x@0.31.9"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extensionsGuardError(dir, false, false); err == nil {
		t.Fatal("配了 packages 却未开扩展时应报错")
	}
}

// 显式开了扩展、显式声明不要扩展、或目录里什么都没配，都不应拦。
func TestExtensionsGuard不误拦(t *testing.T) {
	withPlugins := t.TempDir()
	writeExt(t, withPlugins, "a.ts")
	if err := extensionsGuardError(withPlugins, true, false); err != nil {
		t.Fatalf("--extensions 时不应拦: %v", err)
	}
	if err := extensionsGuardError(withPlugins, false, true); err != nil {
		t.Fatalf("--no-extensions 时不应拦: %v", err)
	}
	empty := t.TempDir()
	if err := extensionsGuardError(empty, false, false); err != nil {
		t.Fatalf("空目录不应拦: %v", err)
	}
}
