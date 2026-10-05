package transport

import (
	"strings"
	"testing"
)

// /ui/sessions 的两个视图都要能从路由走通。
//
// 默认必须是时间线（不带分类就能看到全部会话）：分组视图若成了默认，
// 用户反而要先展开分组才看得到别的项目。view 参数只在显式要求时切换。
func Test会话列表两个视图(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "s1", cwd)

	timeline := getUI(t, s, "/ui/sessions")
	if timeline.Code != 200 {
		t.Fatalf("时间线视图应回 200，得到 %d", timeline.Code)
	}
	body := timeline.Body.String()
	if strings.Contains(body, `class="cwd-group`) {
		t.Fatalf("默认视图不应是分组:\n%s", body)
	}
	if !strings.Contains(body, `data-session="s1"`) {
		t.Fatalf("默认视图应列出会话:\n%s", body)
	}
	// 显式传 timeline 与不传一致。
	explicit := getUI(t, s, "/ui/sessions?view=timeline")
	if strings.Contains(explicit.Body.String(), `class="cwd-group`) {
		t.Fatalf("view=timeline 不应渲染分组:\n%s", explicit.Body.String())
	}

	grouped := getUI(t, s, "/ui/sessions?view=workspace")
	if grouped.Code != 200 {
		t.Fatalf("分组视图应回 200，得到 %d", grouped.Code)
	}
	gb := grouped.Body.String()
	if !strings.Contains(gb, `class="cwd-group"`) {
		t.Fatalf("分组视图应渲染分组:\n%s", gb)
	}
	if !strings.Contains(gb, `data-session="s1"`) {
		t.Fatalf("分组视图也要列出会话:\n%s", gb)
	}
	// 分组视图里筛选下拉被隐藏：分类已由分组标题承担。
	if !strings.Contains(gb, `hx-swap-oob="outerHTML:#cwd-filter-wrap" hidden`) {
		t.Fatalf("分组视图应隐藏工作区下拉:\n%s", gb)
	}
	// 工作区路径出现在组标题的链接里（转义后）。
	if !strings.Contains(gb, "view=timeline") {
		t.Fatalf("组标题应能切回时间线:\n%s", gb)
	}
}

// 未知的 view 值按默认（时间线）处理，不做静默降级到别的视图。
func Test未知视图值走时间线(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "s1", cwd)
	rec := getUI(t, s, "/ui/sessions?view=bogus")
	if rec.Code != 200 {
		t.Fatalf("应回 200，得到 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `class="cwd-group"`) {
		t.Fatalf("未知 view 应退回时间线:\n%s", rec.Body.String())
	}
}

// 会话行带改名/删除操作按钮。按钮不能放在 <a> 里（嵌套交互元素是非法 HTML，
// 浏览器会拆 DOM），所以行是容器 + 行内链接的结构；data-session 留在容器上，
// 前端的选中与点击委托继续可用。
func Test会话行带改名与删除操作(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "s1", cwd)

	body := getUI(t, s, "/ui/sessions").Body.String()
	for _, needle := range []string{
		`data-action="session-rename"`, `data-action="session-delete"`,
		`<div class="session-item`, `session-link`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("时间线行应包含 %q：\n%s", needle, body)
		}
	}
	// 按钮必须位于链接之外：截取链接段，确认它不含操作按钮。
	start := strings.Index(body, `<a class="session-link`)
	end := strings.Index(body[start:], `</a>`)
	if start < 0 || end < 0 {
		t.Fatalf("行内应有 session-link 链接：\n%s", body)
	}
	if link := body[start : start+end]; strings.Contains(link, "session-delete") || strings.Contains(link, "session-rename") {
		t.Fatalf("操作按钮不得嵌在 <a> 内（非法 HTML）：\n%s", link)
	}
	// 分组视图同构。
	grouped := getUI(t, s, "/ui/sessions?view=workspace").Body.String()
	if !strings.Contains(grouped, `data-action="session-rename"`) || !strings.Contains(grouped, `data-action="session-delete"`) {
		t.Fatalf("分组视图的行也应有操作按钮：\n%s", grouped)
	}
}
