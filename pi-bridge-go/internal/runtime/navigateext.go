package runtime

import (
	_ "embed"
	"os"
	"path/filepath"
)

// 桥内跳转扩展（session.navigate 的 Pi 侧实现）。内置成 .mjs 源文件而不是
// Go 字符串：它是一段真实的 Pi 扩展，保留原文件才能被格式化与当作扩展审阅。
//
//go:embed navigate-ext.mjs
var navigateExtSource []byte

// InstallNavigateExt 把内置扩展落到 stateDir，返回 (扩展路径, 结果目录)。
// 每次启动重写扩展文件：内容永远与二进制同版本，不存在「升级了桥、扩展还是旧的」。
// 结果目录在启动时清空：只服务于本次桥运行的 worker，旧文件都是垃圾。
func InstallNavigateExt(stateDir string) (string, string, error) {
	dir := filepath.Join(stateDir, "navigate-ext")
	results := filepath.Join(dir, "results")
	if err := os.MkdirAll(results, 0700); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, "bridge-navigate.mjs")
	if err := os.WriteFile(path, navigateExtSource, 0600); err != nil {
		return "", "", err
	}
	stale, _ := filepath.Glob(filepath.Join(results, "*.json"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return path, results, nil
}
