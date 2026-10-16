package runtime

import (
	_ "embed"
	"os"
	"path/filepath"
)

// 桥内配置重载扩展（config.reload 的 Pi 侧实现）。内置成 .mjs 源文件而不是
// Go 字符串：它是一段真实的 Pi 扩展，保留原文件才能被格式化与当作扩展审阅。
//
//go:embed reload-ext.mjs
var reloadExtSource []byte

// InstallReloadExt 把内置重载扩展落到 stateDir，返回 (扩展路径, 结果目录)。
// 与跳转/捕获扩展同样的约定：每次启动重写扩展文件，结果目录启动时清空。
func InstallReloadExt(stateDir string) (string, string, error) {
	dir := filepath.Join(stateDir, "reload-ext")
	results := filepath.Join(dir, "results")
	if err := os.MkdirAll(results, 0700); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "bridge-reload.mjs")
	if err := os.WriteFile(path, reloadExtSource, 0600); err != nil {
		return "", "", err
	}
	stale, _ := filepath.Glob(filepath.Join(results, "*.json"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return path, results, nil
}
