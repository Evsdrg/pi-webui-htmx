package transport

import (
	"os"
	"testing"

	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/testutil"
)

// uiRenderer 加载 UI 包，供 newTestServer 使用。
//
// 三种结果：
//   - 找到并能加载 → 返回渲染器；
//   - 根本没有 UI 包（单独检出桥、且未设 PI_WEBUI_DIR）→ 返回 nil。
//     UI 层禁用是桥的合法状态（此时只提供 API），相关断言各自跳过；
//   - 目录存在但加载失败 → 失败：那是模板或构建产物有问题，不是环境差异。
//
// 显式设置 PI_WEBUI_DIR 却无效时由 RequireWebUIDir 直接失败——配置写错
// 必须让它响，静默跳过会让整组跨目录测试悄悄消失。
func uiRenderer(t *testing.T) *presentation.Renderer {
	t.Helper()
	dir := testutil.WebUIDir()
	if dir == "" {
		if os.Getenv("PI_WEBUI_DIR") != "" {
			testutil.RequireWebUIDir(t) // 配置无效，必然失败
		}
		return nil
	}
	rendered, err := presentation.LoadFromDir(dir)
	if err != nil {
		t.Fatalf("UI 包在 %s 但加载失败（若缺 dist/assets，先执行 pnpm build）：%v", dir, err)
	}
	return rendered
}

// requireUI 与 uiRenderer 相同，但要求 UI 必须可用——用在断言本身依赖
// 模板渲染的用例上。目录缺失时跳过并说明怎么补齐。
func requireUI(t *testing.T) *presentation.Renderer {
	t.Helper()
	rendered := uiRenderer(t)
	if rendered == nil {
		t.Skip("跳过：未找到 UI 包（设 PI_WEBUI_DIR，或把 pi-webui-htmx 检出放在仓库根）")
	}
	return rendered
}
