package runtime

import (
	_ "embed"
	"os"
	"path/filepath"
)

// 桥内工具集扩展。内置成 .mjs 源文件而不是 Go 字符串：它是一段真实的 Pi 扩展，
// 保留原文件才能被格式化与当作扩展审阅。
//
// 作用：会话启动时把 Pi 当前注册的**全部**工具设为激活（含默认关闭的
// grep/find/ls 与已装插件注册的工具）。Pi 的 --tools 白名单会连带禁用扩展
// 工具，所以走扩展的 pi.setActiveTools() 而不是 CLI 开关。另外给模型没指定
// timeout 的 bash/powershell 调用补 300 秒上限——Pi 本身没有默认超时。
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
