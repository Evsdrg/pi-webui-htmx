package presentation

import (
	"strconv"
	"strings"
)

// StatsRow 是详情表里的一行。
type StatsRow struct {
	Label string
	Value string
}

// StatsSection 是详情表的一组。
type StatsSection struct {
	Title string
	Rows  []StatsRow
}

// StatsData 驱动会话详情片段。
type StatsData struct {
	Sections []StatsSection
	// Error 非空时只显示这一条说明（例如 Pi 无法汇总失败回合）。
	Error string
}

// RenderStats 渲染会话详情（消息、Token、费用、上下文）。
//
// 这些数字全部来自 Pi 的 get_session_stats；桥只做分类与格式化，
// 不在前端重算——前端拿到的就是可以展示的文本。
func (r *Renderer) RenderStats(meta StatsMeta, stats map[string]any, err error) (string, error) {
	data := StatsData{}
	if err != nil || stats == nil {
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Error = "Pi 尚未返回统计信息。"
		}
		return r.execute("stats.html", data)
	}
	data.Sections = statsSections(meta, stats)
	return r.execute("stats.html", data)
}

// StatsMeta 是与统计无关、但同属「这次会话是什么」的事实。
//
// 它包括模型/思考强度/工具预设：这些都是 Pi 与 worker 已经知道的值，
// 因此整个面板都能由桥渲染，前端不必再拼一遍表格。
type StatsMeta struct {
	Name     string
	File     string
	ID       string
	Cwd      string
	Branch   string
	Worktree string
	Model    string
	Thinking string
	Preset   string
	Status   string
}

func statsSections(meta StatsMeta, stats map[string]any) []StatsSection {
	sections := make([]StatsSection, 0, 5)

	session := make([]StatsRow, 0, 4)
	if meta.Name != "" {
		session = append(session, StatsRow{"名称", meta.Name})
	}
	if meta.File != "" {
		session = append(session, StatsRow{"会话文件", meta.File})
	}
	if meta.ID != "" {
		session = append(session, StatsRow{"会话 ID", meta.ID})
	}
	if len(session) > 0 {
		sections = append(sections, StatsSection{Title: "会话", Rows: session})
	}

	project := make([]StatsRow, 0, 3)
	if meta.Cwd != "" {
		project = append(project, StatsRow{"工作目录", meta.Cwd})
	}
	if meta.Branch != "" {
		project = append(project, StatsRow{"Git 分支", meta.Branch})
	}
	if meta.Worktree != "" {
		project = append(project, StatsRow{"Git 工作树", meta.Worktree})
	}
	if len(project) > 0 {
		sections = append(sections, StatsSection{Title: "项目", Rows: project})
	}

	message := []StatsRow{
		{"用户消息", countText(stats["userMessages"])},
		{"助手消息", countText(stats["assistantMessages"])},
		{"工具调用", countText(stats["toolCalls"])},
		{"工具结果", countText(stats["toolResults"])},
		{"消息总数", countText(stats["totalMessages"])},
	}
	sections = append(sections, StatsSection{Title: "消息", Rows: message})

	if tokens := recordOf(stats["tokens"]); len(tokens) > 0 {
		rows := []StatsRow{{"输入", countText(tokens["input"])}, {"输出", countText(tokens["output"])}}
		// 缓存读写只有实际发生时才显示，避免用一串 0 淹没有效信息。
		if numberField(tokens["cacheRead"]) > 0 {
			rows = append(rows, StatsRow{"缓存读取", countText(tokens["cacheRead"])})
		}
		if numberField(tokens["cacheWrite"]) > 0 {
			rows = append(rows, StatsRow{"缓存写入", countText(tokens["cacheWrite"])})
		}
		rows = append(rows, StatsRow{"总计", countText(tokens["total"])})
		sections = append(sections, StatsSection{Title: "Token", Rows: rows})
	}

	runtime := []StatsRow{
		{"模型", orDash(meta.Model)},
		{"思考强度", orDash(meta.Thinking)},
		{"工具预设", orDash(meta.Preset)},
	}
	if meta.Status != "" {
		runtime = append([]StatsRow{{"状态", meta.Status}}, runtime...)
	}
	sections = append(sections, StatsSection{Title: "运行", Rows: runtime})

	extra := make([]StatsRow, 0, 3)
	if cost := numberField(stats["cost"]); cost > 0 {
		extra = append(extra, StatsRow{"费用", "$" + strconv.FormatFloat(cost, 'f', 4, 64)})
	}
	if usage := recordOf(stats["contextUsage"]); len(usage) > 0 {
		percent := "?"
		if value, ok := usage["percent"].(float64); ok {
			percent = strconv.FormatFloat(value, 'f', 1, 64) + "%"
		}
		window := numberField(usage["contextWindow"])
		extra = append(extra, StatsRow{"上下文", percent + " / " + compactNumber(window)})
	}
	if tokens := recordOf(stats["tokens"]); len(tokens) > 0 {
		// 命中率 = 缓存读取 /（输入 + 缓存写入 + 缓存读取），
		// 分母覆盖全部输入类 Token，否则会算出 >100%。
		read, write, input := numberField(tokens["cacheRead"]), numberField(tokens["cacheWrite"]), numberField(tokens["input"])
		if denominator := read + write + input; denominator > 0 && read+write > 0 {
			extra = append(extra, StatsRow{"缓存命中率", strconv.FormatFloat(read/denominator*100, 'f', 1, 64) + "%"})
		}
	}
	if len(extra) > 0 {
		sections = append(sections, StatsSection{Title: "用量", Rows: extra})
	}
	return sections
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// countText 把计数格式化成千位分隔；缺失时给一个明确的占位而不是 0。
func countText(value any) string {
	switch typed := value.(type) {
	case float64:
		return withThousands(int64(typed))
	case int:
		return withThousands(int64(typed))
	case int64:
		return withThousands(typed)
	}
	return "—"
}

func numberField(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}

func withThousands(value int64) string {
	text := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	var builder strings.Builder
	for i, digit := range text {
		if i > 0 && (len(text)-i)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	if negative {
		return "-" + builder.String()
	}
	return builder.String()
}

// compactNumber 把大数字压成 1.2M / 66K 这类短标签。
func compactNumber(value float64) string {
	switch {
	case value >= 1_000_000:
		return strconv.FormatFloat(value/1_000_000, 'f', 1, 64) + "M"
	case value >= 1_000:
		return strconv.FormatFloat(value/1_000, 'f', 0, 64) + "K"
	default:
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
}
