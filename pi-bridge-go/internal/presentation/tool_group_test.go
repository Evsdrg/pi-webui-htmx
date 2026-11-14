// 工具刷屏的三条修法（2026-11-14，实测数据驱动）：
//
//  1. 相邻同名工具自动归组（真实会话里有 bash ×46 的极端案例）；
//  2. todowrite 显示任务清单快照，而不只是一个空行；
//  3. codemode 拆成「每条命令 + 该段输出」，而不是一整块原始文本。
//
// 判据都来自真实会话样本（23 个会话、117,553 次 toolCall）：
//   - 连续同名 run ≥3 才算一组（2 次折叠比展开更烦）；
//   - todowrite 的 todos 数组是**全量替换**语义，只显示最近一次；
//   - codemode 的输出分段标记 `==> text N/M <==` 在 134/161 里存在，
//     且「tools.xxx( 出现次数 == 段数」在 116/161 里成立——只在严格相等
//     时才拆，否则整段呈现（宁可粗，不要错位）。
package presentation

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// toolCallEntry 造一条带 toolCall 的 assistant 条目。
func assistantWithCalls(t *testing.T, id string, calls ...string) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"type":"message","id":"` + id + `","message":{"role":"assistant","content":[` +
		strings.Join(calls, ",") + `]}}`)
}

// toolResultEntry 造一条工具结果。
func toolResultEntry(id, callID, toolName, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "message", "id": id,
		"message": map[string]any{
			"role": "toolResult", "toolCallId": callID, "toolName": toolName,
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	})
	return json.RawMessage(b)
}

// callJSON 造一条 toolCall 块。
func callJSON(id, name string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": args})
	return string(b)
}

// ---- 1. 相邻同名归组 ----

func Test相邻同名工具归组(t *testing.T) {
	renderer := testRenderer(t)
	var entries []json.RawMessage
	entries = append(entries, json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`))
	var calls []string
	for i := 0; i < 5; i++ {
		calls = append(calls, callJSON(callIDN(i), "bash", map[string]any{"command": "echo " + callIDN(i)}))
	}
	entries = append(entries, assistantWithCalls(t, "a1", calls...))
	for i := 0; i < 5; i++ {
		entries = append(entries, toolResultEntry(outIDN(i), callIDN(i), "bash", "ok"+callIDN(i)))
	}
	entries = append(entries, json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完成"}]}}`))

	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: entries})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 必须出现一个聚合组，标题含「bash」与次数 5。
	if !strings.Contains(html, "tool-group") {
		t.Fatalf("5 个连续 bash 应渲染成聚合组：%s", excerpt(html))
	}
	if !strings.Contains(html, "bash ×5") {
		t.Fatalf("聚合组标题应含「bash ×5」：%s", excerpt(html))
	}
}

func callIDN(i int) string { return "call_" + string(rune('a'+i)) }
func outIDN(i int) string  { return "t" + string(rune('a'+i)) }

func Test不同工具不合并(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
		assistantWithCalls(t, "a1",
			callJSON("call_x", "bash", map[string]any{"command": "ls"}),
			callJSON("call_y", "read", map[string]any{"path": "/a"}),
			callJSON("call_z", "bash", map[string]any{"command": "pwd"})),
		toolResultEntry("t1", "call_x", "bash", "a"),
		toolResultEntry("t2", "call_y", "read", "b"),
		toolResultEntry("t3", "call_z", "bash", "c"),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 三个不同类型交替：都不足 3 次连续同名，不应聚合。
	if strings.Contains(html, "tool-group") {
		t.Fatalf("交替的不同工具不该被聚合：%s", excerpt(html))
	}
}

func Test两次同名不聚合(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
		assistantWithCalls(t, "a1",
			callJSON("call_p", "bash", map[string]any{"command": "ls"}),
			callJSON("call_q", "bash", map[string]any{"command": "pwd"})),
		toolResultEntry("t1", "call_p", "bash", "a"),
		toolResultEntry("t2", "call_q", "bash", "b"),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "tool-group") {
		t.Fatalf("2 次同名折叠比展开更烦，不该聚合：%s", excerpt(html))
	}
}

// ---- 2. todowrite 显示任务清单 ----

func Test待办工具显示任务清单(t *testing.T) {
	renderer := testRenderer(t)
	todos := `[{"content":"拉取 ImmortalWrt master","status":"in_progress","priority":"high"},` +
		`{"content":"准备构建依赖","status":"completed","priority":"high"},` +
		`{"content":"构建基础镜像","status":"pending","priority":"medium"}]`
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"开工"}]}}`),
		assistantWithCalls(t, "a1", `{"type":"toolCall","id":"call_td","name":"todowrite","arguments":{"todos":`+todos+`}}`),
		toolResultEntry("t1", "call_td", "todowrite", todos),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := blockByEntry(t, html, "t1")
	// 标题行应给出计数，让折叠态也能看出模型在做什么。
	if !strings.Contains(block, "3 项") {
		t.Fatalf("todowrite 行应显示「3 项」：%s", block)
	}
	// 展开后逐条列出任务内容。
	for _, want := range []string{"拉取 ImmortalWrt master", "准备构建依赖", "构建基础镜像"} {
		if !strings.Contains(block, want) {
			t.Fatalf("todowrite 展开应列出任务「%s」：%s", want, block)
		}
	}
	// 状态要可辨：进行中/已完成/待办各有一种标记（用 data-status，
	// 不用拼接类名——契约脚本按字面量校验，动态类名会被判成死规则）。
	for _, mark := range []string{`data-status="completed"`, `data-status="in_progress"`, `data-status="pending"`} {
		if !strings.Contains(block, mark) {
			t.Fatalf("todowrite 应标出 %s 状态：%s", mark, block)
		}
	}
}

func Test待办工具无任务时不刷空列表(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`),
		assistantWithCalls(t, "a1", `{"type":"toolCall","id":"call_e","name":"todowrite","arguments":{"todos":[]}}`),
		toolResultEntry("t1", "call_e", "todowrite", ""),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := blockByEntry(t, html, "t1")
	if strings.Contains(block, "todo-list") {
		t.Fatalf("空任务列表不应渲染清单：%s", block)
	}
}

// ---- 3. codemode 拆分命令与各自输出 ----

func Test代码模式拆出命令与输出(t *testing.T) {
	renderer := testRenderer(t)
	code := `const r = await Promise.allSettled([tools.grep({path:'/a',pattern:'x'}),tools.ls({path:'/b'})]);` +
		`for(let i=0;i<r.length;i++)text({part:i,result:r[i]});`
	result := "Script completed\nWall time 0.3 seconds\nOutput:\n" +
		"==> text 1/2 <==\n{\"part\":0,\"result\":{\"status\":\"fulfilled\",\"value\":\"grep-hit\"}}\n" +
		"==> text 2/2 <==\n{\"part\":1,\"result\":{\"status\":\"fulfilled\",\"value\":\"ls-out\"}}"
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
		assistantWithCalls(t, "a1", callJSON("call_cm", "codemode", map[string]any{"code": code})),
		toolResultEntry("t1", "call_cm", "codemode", result),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 不解用 blockByEntry：codemode 的每条命令本身是嵌套 <details>，
	// 而那个辅助函数遇到第一个嵌套 <details> 就截断（它服务于「单块无嵌套」）。
	idx := strings.Index(html, `class="code-commands"`)
	if idx < 0 {
		t.Fatalf("codemode 应渲染 code-commands 容器：%s", excerpt(html))
	}
	seg := html[idx:]
	if end := strings.Index(seg, "</details>\n</details>"); end > 0 {
		seg = seg[:end]
	}
	for _, want := range []string{"grep", "ls", "grep-hit", "ls-out"} {
		if !strings.Contains(seg, want) {
			t.Fatalf("codemode 应列出命令与各自输出，缺 %q：%s", want, excerpt(seg))
		}
	}
}

func Test代码模式命令数不匹配时整段呈现(t *testing.T) {
	renderer := testRenderer(t)
	code := `await tools.grep({path:'/a',pattern:'x'}); await tools.ls({path:'/b'});`
	// 只有 1 段输出，但有 2 条命令：不能猜，退回整段。
	result := "Script completed\nOutput:\n==> text 1/1 <==\nmerged"
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
		assistantWithCalls(t, "a1", callJSON("call_cm2", "codemode", map[string]any{"code": code})),
		toolResultEntry("t1", "call_cm2", "codemode", result),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, `class="code-command"`) {
		t.Fatalf("命令数与输出段数不匹配时不该拆分：%s", excerpt(html))
	}
	if !strings.Contains(html, "merged") {
		t.Fatalf("应整段呈现原始输出：%s", excerpt(html))
	}
}

// ---- 4. 无预览工具的标题行 ----

func Test上下文工具标题行有内容(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
		assistantWithCalls(t, "a1",
			`{"type":"toolCall","id":"call_r","name":"ctx_reduce","arguments":{"drop":[12,15,18]}}`,
			`{"type":"toolCall","id":"call_m","name":"ctx_memory","arguments":{"action":"pin","content":"构建前先清空 feeds","category":"build"}}`),
		toolResultEntry("t1", "call_r", "ctx_reduce", "ok"),
		toolResultEntry("t2", "call_m", "ctx_memory", "ok"),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if r := blockByEntry(t, html, "t1"); !strings.Contains(r, "§12") {
		t.Fatalf("ctx_reduce 预览应显示 tag 列表：%s", r)
	}
	if m := blockByEntry(t, html, "t2"); !strings.Contains(m, "构建前先清空 feeds") || !strings.Contains(m, "pin") {
		t.Fatalf("ctx_memory 预览应显示动作与内容：%s", m)
	}
}

func excerpt(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

// 工具带图片时，模板里 {{range .Step.Images}} 的作用域会切换成 int：
// 内层若再引用 .Step 会报 "can't evaluate field Step in type int"。
// 这条回归曾被「无图片夹具」掩盖——真实大会话（1,238 回合）才炸出来。
func Test工具带图片时能正常渲染(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{Entries: []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"看图"}]}}`),
		assistantWithCalls(t, "a1", callJSON("call_i", "read", map[string]any{"path": "/a.png"})),
		json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolCallId":"call_i","toolName":"read","content":[` +
			`{"type":"text","text":"图片"},{"type":"image","data":"AAAA","mimeType":"image/png"}]}}`),
		json.RawMessage(`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"完"}]}}`),
	}})
	if err != nil {
		t.Fatalf("带图片的工具行渲染失败：%v", err)
	}
	if !strings.Contains(html, `data-lazy="tool-image"`) {
		t.Fatalf("应渲染图片占位按钮：%s", excerpt(html))
	}
}
