package presentation

import (
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// 侧栏的工作区筛选控件由桥渲染。
//
// 为什么不交给前端收集：会话列表是分页的，前端只能看到当前页里的 cwd，
// 据此生成的候选会漏掉第二页与筛选后的工作区，计数也会是错的。
func Test会话片段渲染工作区筛选(t *testing.T) {
	r := testRenderer(t)
	list := sessions.Listing{
		Items: []sessions.Header{
			{ID: "a1", Cwd: "/opt/projects/alpha", Name: "会话一"},
			{ID: "b1", Cwd: "/opt/projects/beta", Name: "会话二"},
		},
		Cwd: "/opt/projects/alpha",
		Cwds: []sessions.CwdCount{
			{Cwd: "/opt/projects/alpha", Count: 2},
			{Cwd: "/opt/projects/beta", Count: 1},
		},
	}
	html, err := r.RenderSessionsPage(list, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	// 选项必须随片段一起更新，且带 OOB 目标——否则筛选控件永远停在
	// 外壳里的初始状态（只有「全部工作区」）。
	if !strings.Contains(html, `hx-swap-oob="innerHTML:#session-cwd-filter"`) {
		t.Fatalf("片段应携带工作区下拉的 OOB 更新:\n%s", html)
	}
	if !strings.Contains(html, `value="/opt/projects/alpha"`) || !strings.Contains(html, `value="/opt/projects/beta"`) {
		t.Fatalf("下拉应列出两个工作区:\n%s", html)
	}
	// 计数来自后端统计，不是当前页的行数。
	if !strings.Contains(html, "alpha（2）") || !strings.Contains(html, "beta（1）") {
		t.Fatalf("下拉项应带各自会话数:\n%s", html)
	}
	// 当前筛选要回显为选中，否则用户看不到自己筛在哪里。
	if !strings.Contains(html, `value="/opt/projects/alpha" title="/opt/projects/alpha" selected`) {
		t.Fatalf("当前筛选应标记 selected:\n%s", html)
	}
	// 完整路径放在 title 里：侧栏只有 260px，option 文本放不下，
	// 而 option 不像普通元素那样能靠 CSS 截断。
	if !strings.Contains(html, `title="/opt/projects/alpha"`) {
		t.Fatalf("下拉项应保留完整路径供悬停查看:\n%s", html)
	}
	if !strings.Contains(html, `<option value="">全部工作区</option>`) {
		t.Fatalf("应始终提供「全部工作区」这一项:\n%s", html)
	}
}

// 筛选后「加载更多」必须带上同一个 cwd。
//
// 反例是很容易写出来的：按钮沿用不带 cwd 的 URL，第二页就会跳回全部会话，
// 用户看到的是「翻页以后筛选失效了」。
func Test加载更多带上工作区筛选(t *testing.T) {
	r := testRenderer(t)
	list := sessions.Listing{
		Items:   []sessions.Header{{ID: "a1", Cwd: "/opt/projects/alpha", Name: "会话一"}},
		HasMore: true,
		Cwd:     "/opt/projects/alpha",
		Cwds:    []sessions.CwdCount{{Cwd: "/opt/projects/alpha", Count: 2}},
	}
	html, err := r.RenderSessionsPage(list, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	more := moreButtonURL(t, html)
	if !strings.Contains(more, "offset=1") || !strings.Contains(more, "cwd=") {
		t.Fatalf("「加载更多」应同时带 offset 与 cwd，实际 %q", more)
	}
	// 路径里的 / 应被转义，不能原样拼进查询串（否则参数会被截断）。
	if strings.Contains(more, "cwd=/opt/projects/alpha") {
		t.Fatalf("cwd 应做 URL 转义（/ 不能原样出现在查询串里），实际 %q", more)
	}

	// 未筛选时不加多余的参数。只看按钮的 URL——页面别处本来就有
	// data-cwd 属性与 #session-cwd-filter 这个 id，整体搜 "cwd" 会误判。
	plain := sessions.Listing{
		Items:   []sessions.Header{{ID: "a1", Cwd: "/opt/projects/alpha", Name: "会话一"}},
		HasMore: true,
	}
	html2, err := r.RenderSessionsPage(plain, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := moreButtonURL(t, html2); strings.Contains(got, "cwd=") {
		t.Fatalf("未筛选时不应带 cwd 参数，实际 %q", got)
	}
}

// moreButtonURL 取出「加载更多」按钮的 hx-get 目标。
// 断言 URL 而不是整段 HTML：页面别处也有 cwd 字样，整体搜索会得出错误结论。
func moreButtonURL(t *testing.T, html string) string {
	t.Helper()
	for _, line := range strings.Split(html, "\n") {
		if !strings.Contains(line, "加载更多会话") {
			continue
		}
		start := strings.Index(line, `hx-get="`)
		if start < 0 {
			t.Fatalf("按钮缺少 hx-get：%s", line)
		}
		rest := line[start+len(`hx-get="`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			t.Fatalf("hx-get 引号不闭合：%s", line)
		}
		return rest[:end]
	}
	t.Fatal("HTML 里没有「加载更多会话」按钮")
	return ""
}

// 筛选到空工作区时，空态文案要说明是筛选所致，而不是「还没有会话」——
// 后者会让用户以为会话消失了。
func Test筛选空结果的空态文案(t *testing.T) {
	r := testRenderer(t)
	empty := sessions.Listing{Cwd: "/opt/projects/none", Cwds: []sessions.CwdCount{{Cwd: "/opt/projects/none"}}}
	html, err := r.RenderSessionsPage(empty, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "这个工作区下还没有会话") {
		t.Fatalf("筛选空结果应有专门文案:\n%s", html)
	}
	if strings.Contains(html, "还没有会话。新建一个") {
		t.Fatalf("筛选空结果不应说「还没有会话」:\n%s", html)
	}

	// 未筛选时的空态保持原文案。
	none := sessions.Listing{}
	html2, err := r.RenderSessionsPage(none, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html2, "还没有会话") {
		t.Fatalf("未筛选空结果应保留原文案:\n%s", html2)
	}
}
