package presentation

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// 工具块的成功/失败配色由 toolResult.isError 决定。这条链路断了
// （比如把 isError 读错字段）不会报错，只会让所有工具块都显示成成功，
// 用户因此看不出哪一步失败了——所以必须锁。
func Test工具块按成败着色(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:12.177Z","message":{"role":"toolResult","toolName":"bash","isError":false,"content":[{"type":"text","text":"ok"}]}}`),
			json.RawMessage(`{"type":"message","id":"t2","timestamp":"2026-09-21T13:33:18.311Z","message":{"role":"toolResult","toolName":"grep","isError":true,"content":[{"type":"text","text":"no match"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 工具名必须来自记录里的 toolName，不能一律显示「工具」。
	for _, name := range []string{">bash<", ">grep<"} {
		if !strings.Contains(html, name) {
			t.Fatalf("应显示真实工具名 %s：%s", name, html)
		}
	}
	okBlock := blockByEntry(t, html, "t1")
	errBlock := blockByEntry(t, html, "t2")
	if !strings.Contains(okBlock, `data-ok="true"`) {
		t.Fatalf("成功步骤应标记 data-ok=true：%s", okBlock)
	}
	if !strings.Contains(errBlock, `data-ok="false"`) {
		t.Fatalf("失败步骤应标记 data-ok=false：%s", errBlock)
	}
}

// 时长由条目时间戳推导，不足 1 秒不显示。这条与 Pi Web 的
// `secs > 0 ? secs : undefined` 一致：给 30 毫秒的步骤显示 "0s" 只会干扰阅读。
func Test工具时长按整秒推导且亚秒不显示(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			// 亚秒：assistant 12.135 → 12.177，只有 42 毫秒。
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:12.177Z","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok"}]}}`),
			// 跨秒：同一 assistant 到 18.311，约 6 秒。
			json.RawMessage(`{"type":"message","id":"t2","timestamp":"2026-09-21T13:33:18.311Z","message":{"role":"toolResult","toolName":"grep","content":[{"type":"text","text":"x"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	fast := blockByEntry(t, html, "t1")
	if strings.Contains(fast, "tool-duration") {
		t.Fatalf("亚秒步骤不应显示时长：%s", fast)
	}
	slow := blockByEntry(t, html, "t2")
	if !strings.Contains(slow, "6 秒") {
		t.Fatalf("6 秒步骤应显示「6 秒」：%s", slow)
	}
}

// 思考时长 = 本回合首个 assistant 条目时间戳 − 前一条目时间戳。
// 这正是 Pi Web 的 thinkingDurationFromFile 推导。
func Test思考时长取生成间隔(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":""},{"type":"text","text":"好"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 04.142 → 12.135 约 8 秒。
	if !strings.Contains(html, "持续了 8 秒") {
		t.Fatalf("应显示「持续了 8 秒」的思考时长：%s", html)
	}
}

// 时间戳缺失时不能把零值当成 1970 年算出离谱时长。
func Test时间戳缺失时不显示时长(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "tool-duration") || strings.Contains(html, "thinking-duration") {
		t.Fatalf("无时间戳时不应显示时长：%s", html)
	}
}

// blockByEntry 按 tool-call 的 data-entry-id 取单块。
// 直接搜整篇会把别的步骤的时长算进来。
func blockByEntry(t *testing.T, html, id string) string {
	t.Helper()
	marker := `data-entry-id="` + id + `"`
	start := strings.Index(html, marker)
	if start < 0 {
		t.Fatalf("找不到条目 %s", id)
	}
	// 回到所属的 <details 开头，取到下一个 <details 或 </div> 之前。
	open := strings.LastIndex(html[:start], "<details")
	if open < 0 {
		t.Fatalf("条目 %s 不在 details 内", id)
	}
	rest := html[open:]
	// 从开标签之后开始找下一个 <details：rest 自身就以 <details 开头，
	// 从 0 找会命中自己，于是把后面所有步骤都算进来。
	if end := strings.Index(rest[len("<details"):], "<details"); end >= 0 {
		return rest[:len("<details")+end]
	}
	if end := strings.Index(rest, "</div>"); end > 0 {
		return rest[:end]
	}
	return rest
}

// detailsDepth 返回 marker 处已打开的 <details> 层数（含 marker 自身所在的
// 那一层）。用来验证工具/思考确实嵌在过程组里面、assistant 正文确实在组外，
// 而不是只靠先后顺序猜。
func detailsDepth(block, marker string) (int, bool) {
	pos := strings.Index(block, marker)
	if pos < 0 {
		return 0, false
	}
	prefix := block[:pos]
	return strings.Count(prefix, "<details") - strings.Count(prefix, "</details>"), true
}

// processSummaryText 取出过程组 summary 的文本内容。
func processSummaryText(t *testing.T, block string) string {
	t.Helper()
	marker := `class="process-summary">`
	start := strings.Index(block, marker)
	if start < 0 {
		t.Fatalf("找不到过程摘要：%s", block)
	}
	rest := block[start+len(marker):]
	end := strings.Index(rest, "</summary>")
	if end < 0 {
		t.Fatalf("过程摘要没有闭合：%s", block)
	}
	return rest[:end]
}

// allProcessSummaries 拼接块内全部工作段摘要：一个回合按时间线可能
// 分成多个工作段（正文把它们隔开），计数要跨段汇总。
func allProcessSummaries(block string) string {
	var parts []string
	rest := block
	for {
		marker := `class="process-summary">`
		start := strings.Index(rest, marker)
		if start < 0 {
			break
		}
		rest = rest[start+len(marker):]
		end := strings.Index(rest, "</summary>")
		if end < 0 {
			break
		}
		parts = append(parts, rest[:end])
		rest = rest[end:]
	}
	return strings.Join(parts, " | ")
}

// 工具与思考同属折叠的工作段，assistant 正文段留在各自时间位置上。
// 结构一旦退化成「工具/思考和正文平级且正文全拼到末尾」，长任务的时间线
// 就读不出先后了。
func Test工具与思考同组且默认折叠(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":""},{"type":"text","text":"先看看"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:12.177Z","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok"}]}}`),
			json.RawMessage(`{"type":"message","id":"a2","timestamp":"2026-09-21T13:33:18.311Z","message":{"role":"assistant","content":[{"type":"text","text":"完成"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	// 默认折叠：开标签必须一字不差地没有 open。
	if !strings.Contains(block, `<details class="turn-process" data-process-group>`) {
		t.Fatalf("工作段应是未展开的 details.turn-process[data-process-group]：%s", block)
	}
	if !strings.Contains(block, `class="process-summary"`) || !strings.Contains(block, `class="process-body"`) {
		t.Fatalf("工作段缺少 summary.process-summary 或 div.process-body：%s", block)
	}
	// 工具与思考嵌在工作段里：层数为 2（工作段 + 自身）。
	if d, ok := detailsDepth(block, "tool-call"); !ok || d != 2 {
		t.Fatalf("工具必须嵌在工作段内，details 层数应为 2，实际 %d：%s", d, block)
	}
	if d, ok := detailsDepth(block, "thinking-block"); !ok || d != 2 {
		t.Fatalf("思考必须嵌在工作段内，details 层数应为 2，实际 %d：%s", d, block)
	}
	// assistant 正文在工作段外：层数为 0。
	if d, ok := detailsDepth(block, "turn-assistant"); !ok || d != 0 {
		t.Fatalf("assistant 正文必须在工作段之外，details 层数应为 0，实际 %d：%s", d, block)
	}
	// 摘要折叠时可见：工具/思考计数都在 summary 里（可能分布在多个工作段）。
	summary := allProcessSummaries(block)
	if !strings.Contains(summary, "1 个工具") || !strings.Contains(summary, "1 段思考") {
		t.Fatalf("摘要应含工具与思考计数：%q", summary)
	}
}

// 纯思考回合同样要有过程组，且只有思考、没有工具。
func Test纯思考回合渲染过程组(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"想一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":""},{"type":"text","text":"结论"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	if !strings.Contains(block, `class="turn-process"`) {
		t.Fatalf("纯思考回合必须有过程组：%s", block)
	}
	if strings.Contains(block, "tool-call") {
		t.Fatalf("纯思考回合的过程组不应含工具：%s", block)
	}
	if d, ok := detailsDepth(block, "thinking-block"); !ok || d != 2 {
		t.Fatalf("思考应嵌在过程组内：%s", block)
	}
	if !strings.Contains(processSummaryText(t, block), "1 段思考") {
		t.Fatalf("纯思考摘要应报思考段数：%s", block)
	}
}

// 纯正文回合不得生成空过程组。
func Test纯正文回合无空过程组(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"你好"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"你好，有什么可以帮你"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	if strings.Contains(block, "turn-process") || strings.Contains(block, "process-body") {
		t.Fatalf("纯正文回合不应有空过程组：%s", block)
	}
	if !strings.Contains(block, "turn-assistant") {
		t.Fatalf("纯正文回合仍应渲染正文：%s", block)
	}
}

// 工具正文里的危险内容必须被转义，且只归属在工具块内——
// 既不能变成可执行标签，也不能泄进由 Go 计数生成的摘要。
func Test过程组内容转义且归属正确(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"<script>alert(1)</script>"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	if strings.Contains(block, "<script>") {
		t.Fatalf("工具正文不得原样输出可执行标签：%s", block)
	}
	if !strings.Contains(block, "&lt;script&gt;") {
		t.Fatalf("工具正文应被转义：%s", block)
	}
	if strings.Contains(processSummaryText(t, block), "script") {
		t.Fatalf("摘要由计数生成，不得含工具正文内容：%s", block)
	}
}

// 一个回合只出一个过程组：最终正文之前的工具、思考与**中间正文**全部折进去
// （按时间顺序），最终正文留在组外始终可见。中间正文曾单独成气泡常驻显示，
// 现在统一折进过程组——对齐 ZCode「运行中摊开、结算后整体收起」。
func Test中间正文与工具折进同一过程组(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:00Z","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:05Z","message":{"role":"assistant","content":[{"type":"text","text":"先看看目录"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:07Z","message":{"role":"toolResult","toolName":"read","content":[{"type":"text","text":"输出一"}]}}`),
			json.RawMessage(`{"type":"message","id":"a2","timestamp":"2026-09-21T13:33:12Z","message":{"role":"assistant","content":[{"type":"text","text":"再改配置"}]}}`),
			json.RawMessage(`{"type":"message","id":"t2","timestamp":"2026-09-21T13:33:15Z","message":{"role":"toolResult","toolName":"edit","content":[{"type":"text","text":"输出二"}]}}`),
			json.RawMessage(`{"type":"message","id":"a3","timestamp":"2026-09-21T13:33:20Z","message":{"role":"assistant","content":[{"type":"text","text":"结论"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	if n := strings.Count(block, `class="turn-process"`); n != 1 {
		t.Fatalf("一个回合只应有一个过程组，实际 %d 个：%s", n, block)
	}
	group := strings.Index(block, `class="turn-process"`)
	answer := strings.Index(block, `class="turn-assistant"`)
	if group < 0 || answer < 0 || group > answer {
		t.Fatalf("过程组应在最终正文之前：%s", block)
	}
	// 过程组内按时间顺序：中间正文1 → 工具1 → 中间正文2 → 工具2。
	pos := group
	for _, needle := range []string{"先看看目录", "输出一", "再改配置", "输出二"} {
		i := strings.Index(block[pos:], needle)
		if i < 0 {
			t.Fatalf("过程组内缺少或顺序错误：%q：%s", needle, block)
		}
		pos += i + len(needle)
		if pos > answer {
			t.Fatalf("中间内容应全部落在过程组内（最终正文之前）：%s", block)
		}
	}
	// 最终正文在组外；过程组内不得出现它。
	if !strings.Contains(block[answer:], "结论") {
		t.Fatalf("最终正文应在过程组之外：%s", block)
	}
	if strings.Contains(block[group:answer], "结论") {
		t.Fatalf("最终正文不应折进过程组：%s", block)
	}
	// 摘要把工具与中间正文都算进去。
	if summary := processSummaryText(t, block); !strings.Contains(summary, "2 个工具") || !strings.Contains(summary, "2 段说明") {
		t.Fatalf("过程组摘要应计入工具与说明段：%q", summary)
	}
}

// 没有最终正文的回合（中断、纯工具）整个 Flow 都是过程：仍出一个过程组，
// 不生成空的最终正文气泡。
func Test无正文回合整个折进过程组(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:00Z","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:07Z","message":{"role":"toolResult","toolName":"read","content":[{"type":"text","text":"输出一"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := turnBlock(t, html, "u1")
	if n := strings.Count(block, `class="turn-process"`); n != 1 {
		t.Fatalf("无正文回合仍应出一个过程组，实际 %d：%s", n, block)
	}
	if strings.Contains(block, `class="turn-assistant"`) {
		t.Fatalf("无正文回合不应有最终正文气泡：%s", block)
	}
	if !strings.Contains(block, "输出一") {
		t.Fatalf("过程组应含工具：%s", block)
	}
}
