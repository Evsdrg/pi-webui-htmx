package presentation

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// uiDir 是相邻检出的 UI 包。从本包目录往上三层才是工作区根，
// 写两层会指向 pi-bridge-go/pi-webui-htmx，永远命中不了（旧测试就是这样静默跳过的）。
const uiDir = "../../../pi-webui-htmx"

// testRenderer 从真实 UI 包加载模板：模板缺失时这些断言就没有意义，
// 所以检出不存在时跳过，存在但加载失败时直接失败。
func testRenderer(t *testing.T) *Renderer {
	t.Helper()
	if _, err := os.Stat(uiDir); err != nil {
		t.Skip("需要 pi-webui-htmx 检出")
	}
	r, err := LoadFromDir(uiDir)
	if err != nil {
		t.Fatalf("加载 UI 包失败：%v", err)
	}
	return r
}

// Git 变更列表改由桥渲染后，截断语义必须在这里守住：
// 列表被截断时不能宣称「工作区干净」，否则用户会以为没有未提交改动。
func TestGit截断时不宣称工作区干净(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderGitStatus(map[string]any{
		"branch": "main", "clean": true, "truncated": true,
		"files": []map[string]string{{"status": "M", "path": "a.go"}},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "列表已截断") {
		t.Fatalf("截断必须说明列表被截断，实际：%s", html)
	}
	if strings.Contains(html, "工作区干净") {
		t.Fatalf("截断状态不得宣称工作区干净：%s", html)
	}
}

// 未截断且无变更时才显示「工作区干净」。
func TestGit干净状态(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderGitStatus(map[string]any{"branch": "main", "clean": true, "files": []any{}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "工作区干净") {
		t.Fatalf("干净工作区应明确说明，实际：%s", html)
	}
}

// 搜索结果以前在前端用 createElement 拼装；现在由桥渲染，
// 标题与目录必须进 HTML，否则点开后又退化成会话 ID（旧缺陷）。
func Test搜索片段包含标题与目录(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderSearch("问题", []map[string]any{
		{"sessionId": "s1", "entryId": "e9", "title": "示例会话", "cwd": "/repo", "snippet": "命中的这句话"},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{"示例会话", "/repo", "命中的这句话", `data-entry-id="e9"`, `data-session="s1"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("搜索结果缺少 %q：%s", want, html)
		}
	}
}

// 空结果要有可读提示，而不是一片空白。
func Test搜索空结果有提示(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderSearch("找不到", nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "没有匹配") {
		t.Fatalf("空结果应给出提示：%s", html)
	}
}

// 分支树以前在前端摊平 + 拼 DOM，深度只受堆限制（U08 修过一次栈溢出）。
// 改成 Go 之后同样必须用显式栈：20 万层线性链不能让测试进程崩掉。
func Test分支树深线性链不爆栈(t *testing.T) {
	var node map[string]any
	node = map[string]any{"entry": map[string]any{"type": "message", "id": "leaf"}, "children": []any{}}
	const depth = 200_000
	for i := 0; i < depth; i++ {
		node = map[string]any{
			"entry":    map[string]any{"type": "message", "id": "n"},
			"children": []any{node},
		}
	}
	rows, _ := BranchRows(map[string]any{"tree": []any{node}, "leafId": "leaf"}, nil, "leaf")
	if len(rows) != depth+1 {
		t.Fatalf("摊平结果应为 %d 行，实际 %d", depth+1, len(rows))
	}
	if rows[0].Level != 1 {
		t.Fatalf("根层级应为 1，实际 %d", rows[0].Level)
	}
	// 缩进在 branchMaxLevel 封顶，否则长会话会被一层层内边距挤出屏幕。
	if rows[depth].Level != branchMaxLevel {
		t.Fatalf("末行层级应封顶在 %d，实际 %d", branchMaxLevel, rows[depth].Level)
	}
	if rows[5].Level != 6 {
		t.Fatalf("封顶前层级应逐层递增，第 6 行应为 6，实际 %d", rows[5].Level)
	}
}

// 分叉点要标出子节点数量，当前叶子要标出来。
func Test分支树标注分叉与当前叶子(t *testing.T) {
	tree := map[string]any{"tree": []any{map[string]any{
		"entry": map[string]any{"type": "message", "id": "u1", "message": map[string]any{"role": "user", "content": "第一个问题"}},
		"children": []any{
			map[string]any{"entry": map[string]any{"type": "message", "id": "a1"}, "children": []any{}},
			map[string]any{"entry": map[string]any{"type": "message", "id": "a1b"}, "children": []any{}},
		},
	}}, "leafId": "a1"}
	rows, _ := BranchRows(tree, nil, "a1")
	if len(rows) != 3 {
		t.Fatalf("应为 3 行，实际 %d", len(rows))
	}
	if !strings.Contains(rows[0].Kind, "⑂2") {
		t.Fatalf("分叉点应标注子节点数，实际 %q", rows[0].Kind)
	}
	if !rows[1].Current || rows[2].Current {
		t.Fatalf("只有叶子 a1 应标为当前：%+v", rows)
	}
}

// fork_messages 的形状随桥版本不同（裸数组 / {messages:[]}），
// 两种都要接受，否则分支列表会空白。
func Test分支接受两种fork形状(t *testing.T) {
	wrapped := map[string]any{"messages": []any{map[string]any{"entryId": "u1", "text": "第一个问题"}}}
	if _, forks := BranchRows(map[string]any{}, wrapped, ""); len(forks) != 1 || forks[0].EntryID != "u1" {
		t.Fatalf("包装形状解析失败：%+v", forks)
	}
	// 没有 messages 字段时退化为空列表，而不是把对象当消息渲染。
	if _, forks := BranchRows(map[string]any{}, map[string]any{"other": 1}, ""); len(forks) != 0 {
		t.Fatalf("无 messages 字段时不应产生分支行：%+v", forks)
	}
	// entryId 缺失时用正文兜底，按钮仍可用（前端按 entryId 判禁用）。
	if _, forks := BranchRows(map[string]any{}, map[string]any{"messages": []any{map[string]any{"text": "只有正文"}}}, ""); len(forks) != 1 || forks[0].Text != "只有正文" {
		t.Fatalf("缺 entryId 时应用正文兜底：%+v", forks)
	}
}

// 空树与空 fork 都要有可读提示。
func Test分支空状态有提示(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderBranch(nil, nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "还没有分支结构") || !strings.Contains(html, "没有可分支的用户消息") {
		t.Fatalf("空状态应给出提示：%s", html)
	}
}

// 会话详情：布局对齐 Pi Web 的会话弹层（左栏事实 + 中栏消息 + 右栏 Token）。
// 可复制的那几行必须带复制按钮，否则用户没法把会话路径/ID 直接拿去用。
func Test详情可复制行带按钮(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderStats(StatsMeta{
		Name: "示例", File: "/tmp/s.jsonl", ID: "abc123",
		Cwd: "/repo", Branch: "main", Model: "m", Thinking: "off", Preset: "default",
	}, map[string]any{
		"userMessages": 3.0, "assistantMessages": 4.0, "totalMessages": 7.0,
		"tokens": map[string]any{"input": 10.0, "output": 20.0, "total": 30.0},
	}, nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{`data-copy-value="/tmp/s.jsonl"`, `data-copy-value="abc123"`, `data-copy-value="/repo"`, `data-copy-value="main"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("缺少复制按钮 %s：%s", want, html)
		}
	}
	// 三栏结构：左栏事实、消息、Token。
	for _, want := range []string{"stats-info", "消息", "Token"} {
		if !strings.Contains(html, want) {
			t.Fatalf("详情布局缺少 %q：%s", want, html)
		}
	}
}

// 统计失败时仍要给出会话事实：失败回合会让 get_session_stats 整体报错，
// 那时面板至少该说清「这是哪个会话」。
func Test详情统计失败仍显示事实(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderStats(StatsMeta{ID: "abc123", Cwd: "/repo"}, nil, nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "abc123") || !strings.Contains(html, "/repo") {
		t.Fatalf("统计缺失时仍应显示会话事实：%s", html)
	}
}

// 数字格式对齐 Pi Web：千位分隔、上下文窗口用紧凑后缀（小写 k）、百分比一位小数。
// 这些是「同一份数据两边看起来要一样」的直接体现，写死在测试里避免漂移。
func Test详情数字格式对齐(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderStats(StatsMeta{ID: "abc123"},
		map[string]any{
			"userMessages": 1234.0, "totalMessages": 1234567.0,
			"cost":         0.0031,
			"tokens":       map[string]any{"input": 0.0, "output": 0.0, "total": 0.0},
			"contextUsage": map[string]any{"percent": 1.9, "contextWindow": 65536.0},
		}, nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{"1,234", "1,234,567", "$0.0031", "1.9% / 66k"} {
		if !strings.Contains(html, want) {
			t.Fatalf("缺少 %q：%s", want, html)
		}
	}
	if strings.Contains(html, "66K") {
		t.Fatalf("千位后缀应为小写 k（与 Pi Web 的 formatCompact 一致）：%s", html)
	}
}

// 「编辑并重发」只对用户消息有意义：它把原消息文本放回输入框。
// 对 assistant 条目提供它没有语义（Pi 的 fork 只接受用户 entry），
// 而且会把「改写我的提问」误导成「改写 AI 的回答」。
//
// 注意回合结构：assistant 条目会并进前一个用户轮，所以「没有 fork 按钮」
// 只能出现在两种位置——并进用户轮的 assistant 文本（按钮属于该用户轮），
// 以及没有任何 user 锚点的孤儿 assistant 轮。
func Test编辑并重发只出现在用户轮(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			// 孤儿 assistant 必须排在最前：GroupTurns 的 current 初值是 -1，
			// 只有此时它才单独成轮；排在用户轮之后会被并进去。
			json.RawMessage(`{"type":"message","id":"a0","message":{"role":"assistant","content":[{"type":"text","text":"无主回答"}]}}`),
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"我的问题"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"AI 的回答"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if got := strings.Count(html, `data-action="fork"`); got != 1 {
		t.Fatalf("只应有一个用户轮带「编辑并重发」，实际 %d：%s", got, html)
	}
	// 孤儿 assistant 轮必须只有复制按钮。
	block := turnBlock(t, html, "a0")
	if strings.Contains(block, `data-action="fork"`) {
		t.Fatalf("助手轮不应有「编辑并重发」：%s", block)
	}
	if !strings.Contains(block, `data-action="copy-turn"`) {
		t.Fatalf("助手轮应保留复制按钮：%s", block)
	}
	// 用户轮两者都有。
	block = turnBlock(t, html, "u1")
	for _, want := range []string{`data-action="fork"`, `data-action="copy-turn"`} {
		if !strings.Contains(block, want) {
			t.Fatalf("用户轮应含 %s：%s", want, block)
		}
	}
}

// turnBlock 取出单个回合的 HTML。按 data-turn-id 切分，
// 直接搜整篇会把别的回合的按钮算进来。
func turnBlock(t *testing.T, html, id string) string {
	t.Helper()
	marker := `data-turn-id="` + id + `"`
	start := strings.Index(html, marker)
	if start < 0 {
		t.Fatalf("找不到回合 %s", id)
	}
	rest := html[start+len(marker):]
	// 切到下一個 <article>：同一个 id 不会出现两次，按原 id 找会把
	// 后面所有回合都算进来（第一版就是这样，误判助手轮带了 fork 按钮）。
	if end := strings.Index(rest, "<article"); end >= 0 {
		return rest[:end]
	}
	return rest
}
