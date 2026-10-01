package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ModuleRoot 从当前工作目录向上找到含 go.mod 的目录（即 pi-bridge-go 模块根）。
//
// 测试的工作目录是包所在目录（如 internal/transport），需要它来定位与模块
// 平级的 UI 目录，或从模块根解析 PI_WEBUI_DIR 里的相对路径。
func ModuleRoot() (string, error) { return findRepoRoot() }

// RepoRoot 返回包含本模块的仓库根，即模块根的上一级。
// 单仓布局下 pi-bridge-go 与 pi-webui-htmx 都直接位于仓库根。
func RepoRoot() (string, error) {
	module, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(module)
	// 防御：模块就位于文件系统根时，上一级还是自己。
	if parent == module {
		return "", fmt.Errorf("模块根 %s 没有上一级目录", module)
	}
	return parent, nil
}

// WebUIDir 返回 UI 包目录，找不到时返回空串。
//
// 顺序：
//  1. PI_WEBUI_DIR —— 显式配置优先；相对路径按**模块根**解析。
//     直接按 cwd 解析会得到 internal/pi-webui-htmx 这种不存在的路径，
//     因为 go test 以包目录为工作目录。
//  2. 单仓布局推断 —— <仓库根>/pi-webui-htmx。两个目录在同一仓库里固定相邻，
//     因此新克隆的仓库无需任何环境变量即可跑全部跨目录测试。
//
// 只检查目录是否存在，不检查是否已构建（那属于 LoadFromDir 的职责，
// 它的错误信息更具体）。
func WebUIDir() string {
	if dir := os.Getenv("PI_WEBUI_DIR"); dir != "" {
		if filepath.IsAbs(dir) {
			return existingDir(dir)
		}
		if module, err := findRepoRoot(); err == nil {
			return existingDir(filepath.Join(module, dir))
		}
		return existingDir(dir)
	}
	repo, err := RepoRoot()
	if err != nil {
		return ""
	}
	return existingDir(filepath.Join(repo, "pi-webui-htmx"))
}

// existingDir 在路径存在且是目录时返回原值，否则返回空串。
func existingDir(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	return path
}

// RequireWebUIDir 返回 UI 包目录，并区分两种"找不到"：
//
//   - **显式设置了 PI_WEBUI_DIR 但无效** → 让测试失败。这是配置写错，
//     静默跳过会让整个跨目录测试组在 CI 上悄悄消失（本仓库真实踩过：
//     一个少一层的相对路径让 38 个测试从未在本地运行）。
//   - **没有设置且单仓布局也推断不出** → 跳过：这份检出确实不含 UI 包。
func RequireWebUIDir(tb testing.TB) string {
	tb.Helper()
	if explicit := os.Getenv("PI_WEBUI_DIR"); explicit != "" {
		if dir := WebUIDir(); dir != "" {
			return dir
		}
		tb.Fatalf("PI_WEBUI_DIR=%q 不是有效目录（相对路径按模块根 %s 解析）", explicit, moduleRootOrUnset())
	}
	dir := WebUIDir()
	if dir == "" {
		tb.Skip("跳过：未找到 UI 包（设 PI_WEBUI_DIR，或把 pi-webui-htmx 检出放在仓库根）")
	}
	return dir
}

// moduleRootOrUnset 供错误信息使用；解析失败时返回"未知"。
func moduleRootOrUnset() string {
	if root, err := findRepoRoot(); err == nil {
		return root
	}
	return "未知"
}
