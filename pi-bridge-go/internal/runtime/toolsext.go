package runtime

import (
	_ "embed"
	"os"
	"path/filepath"
)

// 桥内工具集扩展（read-only / full 预设的工具名单施加点）。内置成 .mjs 源文件
// 而不是 Go 字符串：它是一段真实的 Pi 扩展，保留原文件才能被格式化与当作扩展审阅。
//
//go:embed tools-ext.mjs
var toolsExtSource []byte

// InstallToolsExt 把内置工具集扩展落到 stateDir，返回扩展路径。
// 与跳转/捕获扩展同样的约定：每次启动重写，内容永远与二进制同版本。
func InstallToolsExt(stateDir string) (string, error) {
	dir := filepath.Join(stateDir, "tools-ext")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "bridge-tools.mjs")
	if err := os.WriteFile(path, toolsExtSource, 0600); err != nil {
		return "", err
	}
	return path, nil
}
