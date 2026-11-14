// todowrite 与 codemode 的结构化解析。
//
// 这两个工具是「一行 `工具名`」最没信息量的两种：todowrite 的真实内容
// 是一份任务清单，codemode 的真实内容是一段脚本 + 若干条命令的输出。
// 历史里把它们压成一行，等于把「模型具体做了什么」整个丢掉。
//
// 解析原则：**只做能确定的解析**。参数或输出形态与预期不符时返回空，
// 渲染层据此退回原来的纯文本呈现——宁可粗一点，也不要把错位的内容
// 当成事实展示。
package presentation

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// parseTodos 解析 todowrite 的 todos 参数。
//
// 语义提醒：todowrite 是**整体替换**，每次调用的 todos 都是当时全量，
// 因此这里只返回本次快照，不做跨调用累加。
func parseTodos(args json.RawMessage) []TodoItem {
	if len(args) == 0 {
		return nil
	}
	var payload struct {
		Todos []struct {
			Content  string `json:"content"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		} `json:"todos"`
	}
	if json.Unmarshal(args, &payload) != nil || len(payload.Todos) == 0 {
		return nil
	}
	out := make([]TodoItem, 0, len(payload.Todos))
	for _, t := range payload.Todos {
		content := strings.TrimSpace(t.Content)
		if content == "" {
			continue
		}
		out = append(out, TodoItem{Content: content, Status: normalizeTodoStatus(t.Status), Priority: strings.TrimSpace(t.Priority)})
	}
	if len(out) == 0 {
		return nil
	}
	// 按「进行中 → 待办 → 已完成 → 已取消」排序：折叠标题行只显示计数，
	// 展开后最该先看到的是正在做的事。同状态内保持原顺序（稳定排序）。
	rank := map[string]int{"in_progress": 0, "pending": 1, "completed": 2, "cancelled": 3}
	stableSortBy(out, func(a, b TodoItem) bool { return rank[a.Status] < rank[b.Status] })
	return out
}

// normalizeTodoStatus 把状态收敛到四种已知值。
// 未知状态归为 pending：它最接近「还没做完」，不会让人误以为已完成。
func normalizeTodoStatus(s string) string {
	switch strings.TrimSpace(s) {
	case "in_progress", "completed", "cancelled":
		return strings.TrimSpace(s)
	default:
		return "pending"
	}
}

// stableSortBy 是插入排序：清单通常只有几条，且必须稳定（相同键保序）。
func stableSortBy[T any](items []T, less func(a, b T) bool) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// todoCounts 汇总各状态条数，供折叠标题行使用。
func todoCounts(items []TodoItem) (total, done, active, pending int) {
	total = len(items)
	for _, t := range items {
		switch t.Status {
		case "completed":
			done++
		case "in_progress":
			active++
		case "pending":
			pending++
		}
	}
	return
}

// codeToolCallRe 匹配脚本里的 tools.<name>( 调用。
//
// 只认这种形态：codemode 的脚本 API 就是 `tools.<name>(...)`，
// 所以它是可靠的工具名来源。参数体不在这里解析——嵌套括号、模板字符串
// 会让正则失准，而参数原文截取已经够看清「在调什么」。
var codeToolCallRe = regexp.MustCompile(`\btools\.([a-zA-Z_][\w]*)\s*\(`)

// codeToolNames 提取脚本里调用的工具名序列（按出现顺序）。
func codeToolNames(code string) []string {
	matches := codeToolCallRe.FindAllStringSubmatch(code, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// codeArgText 取某次调用后面的参数原文（不解析），用于展开时显示「传了什么」。
// 从 `tools.name(` 之后开始，按括号配对截断；配对失败时取到行尾。
func codeArgText(code string, name string, occurrence int) string {
	locs := codeToolCallRe.FindAllStringSubmatchIndex(code, -1)
	seen := 0
	for _, loc := range locs {
		if code[loc[2]:loc[3]] != name {
			continue
		}
		if seen < occurrence {
			seen++
			continue
		}
		start := loc[1]
		depth, i := 1, start
		for i < len(code) && depth > 0 {
			switch code[i] {
			case '(', '{', '[':
				depth++
			case ')', '}', ']':
				depth--
				if depth == 0 {
					return clip(strings.TrimSpace(code[start:i]), 400)
				}
			}
			i++
		}
		end := strings.IndexByte(code[start:], '\n')
		if end < 0 {
			end = len(code) - start
		}
		return clip(strings.TrimSpace(code[start:start+end]), 400)
	}
	return ""
}

// codeSegmentRe 匹配 codemode 输出里的分段标记 `==> text N/M <==`。
var codeSegmentRe = regexp.MustCompile(`(?m)^==> text (\d+)/(\d+) <==$`)

// splitCodeOutput 按分段标记切分 codemode 的输出正文。
// 返回的每段已去掉标记行本身；没有标记时返回 nil。
func splitCodeOutput(text string) []string {
	locs := codeSegmentRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	out := make([]string, 0, len(locs))
	for i, loc := range locs {
		start := loc[1]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := strings.TrimSpace(text[start:end])
		// 分段标记与内容之间可能有一行空的 Output: 残留，去掉首尾空行即可。
		out = append(out, body)
	}
	return out
}

// parseCodeCommands 把 codemode 的脚本与输出配成「每条命令 + 该段输出」。
//
// 只在**段数严格等于命令数**时才配对：分段标记的序号是「第几个 text() 输出」，
// 与「第几个 tools.xxx( 调用」之所以能对上，是因为脚本普遍写成
// `Promise.allSettled([...])` 后按同一顺序 `text({part:i, result:r[i]})`。
// 这个前提在实测样本里成立 116/161，但脚本可以写成别的形态——所以数量不等
// 就不猜，整段呈现。
func parseCodeCommands(code, output string) []CodeCommand {
	names := codeToolNames(code)
	if len(names) == 0 {
		return nil
	}
	segments := splitCodeOutput(output)
	if len(segments) != len(names) {
		return nil
	}
	out := make([]CodeCommand, 0, len(names))
	occurrences := map[string]int{}
	for i, name := range names {
		occ := occurrences[name]
		occurrences[name] = occ + 1
		out = append(out, CodeCommand{Name: name, Input: codeArgText(code, name, occ), Output: segments[i]})
	}
	return out
}

// todoSummary 生成 todowrite 折叠标题行的摘要：「3 项 · 1 进行中 · 1 已完成」。
//
// 折叠态只能看到这一行，所以它必须回答「模型在做什么」——只写工具名等于
// 什么都没说。进行中优先展示：那是当前真正在推进的事。
func todoSummary(items []TodoItem) string {
	total, done, active, pending := todoCounts(items)
	if total == 0 {
		return ""
	}
	parts := []string{itoa(total) + " 项"}
	if active > 0 {
		parts = append(parts, itoa(active)+" 进行中")
	}
	if done > 0 {
		parts = append(parts, itoa(done)+" 已完成")
	}
	if pending > 0 && active == 0 {
		// 只有没有进行中项时才说「待办」：两者同时出现时，「进行中」
		// 是更该被看见的信息，避免标题行过长。
		parts = append(parts, itoa(pending)+" 待办")
	}
	return strings.Join(parts, " · ")
}

// codeSummary 生成 codemode 折叠标题行的摘要：「read, grep, ls · 3 条命令」。
//
// 工具名去重后按首次出现顺序列出，最多 4 个：一行放得下，也足够让人
// 判断这段脚本在做什么。超过 4 个时以「等」收尾。
func codeSummary(cmds []CodeCommand) string {
	if len(cmds) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(cmds))
	names := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	const maxNames = 4
	suffix := ""
	if len(names) > maxNames {
		names = names[:maxNames]
		suffix = " 等"
	}
	return strings.Join(names, ", ") + suffix + " · " + itoa(len(cmds)) + " 条命令"
}

// itoa 是 strconv.Itoa 的短别名，让上面的拼接读起来不至于被转换淹没。
func itoa(n int) string { return strconv.Itoa(n) }
