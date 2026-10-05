package runtime

import (
	_ "embed"
	"os"
	"path/filepath"
)

// 桥内捕获扩展（/ui/system 与 /ui/tools 的 Pi 侧实现）。内置成 .mjs 源文件而
// 不是 Go 字符串：它是一段真实的 Pi 扩展，保留原文件才能被格式化与当作扩展审阅。
//
//go:embed capture-ext.mjs
var captureExtSource []byte

// InstallCaptureExt 把内置捕获扩展落到 stateDir，返回 (扩展路径, 结果目录)。
// 与跳转扩展同样的约定：每次启动重写扩展文件，结果目录启动时清空。
func InstallCaptureExt(stateDir string) (string, string, error) {
	dir := filepath.Join(stateDir, "capture-ext")
	results := filepath.Join(dir, "results")
	if err := os.MkdirAll(results, 0700); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "bridge-capture.mjs")
	if err := os.WriteFile(path, captureExtSource, 0600); err != nil {
		return "", "", err
	}
	stale, _ := filepath.Glob(filepath.Join(results, "*.json"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return path, results, nil
}
