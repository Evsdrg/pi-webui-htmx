package presentation

import (
	"strconv"
	"strings"
)

// StatsField 是详情里的一行。
type StatsField struct {
	Label string
	Value string
	// Copy 非空时在该行右侧给一个复制按钮；值为要复制的原文。
	Copy string
	// CopyLabel 是复制按钮提示里的宾语（例如「会话 ID」），
	// 提示文案由它拼成「复制会话 ID」/「已复制会话 ID」。
	CopyLabel string
}

// StatsSection 是详情里的一组。
type StatsSection struct {
	Title  string
	Fields []StatsField
}

// StatsData 驱动会话详情片段。
//
// 布局对齐 Pi Web 的会话弹层：左栏是「会话 / 项目」事实（带复制按钮），
// 中栏是消息计数，右栏是 Token 与用量（右对齐、紧凑）。
type StatsData struct {
	Info    []StatsSection
	Message *StatsSection
	Token   *StatsSection
	// Error 非空时只显示这一条说明（例如 Pi 无法汇总失败回合）。
	Error string
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

// RenderStats 渲染会话详情。
//
// 数字全部来自 Pi 的 get_session_stats；桥只做分类与格式化，
// 不在前端重算——前端拿到的就是可以展示的文本。
func (r *Renderer) RenderStats(meta StatsMeta, stats map[string]any, err error) (string, error) {
	// 会话与项目事实不依赖统计，先无条件建立：
	// 含失败回合的会话会让 get_session_stats 整体报错，那时面板
	// 至少还该说清「这是哪个会话」，而不是只剩一句错误。
	data := StatsData{Info: infoSections(meta)}
	if err != nil {
		data.Error = err.Error()
		return r.execute("stats.html", data)
	}
	if stats == nil {
		data.Error = "Pi 尚未返回统计信息。"
		return r.execute("stats.html", data)
	}
	data.Message = newStatsSection("消息", []StatsField{
		{Label: "用户消息", Value: countText(stats["userMessages"])},
		{Label: "助手消息", Value: countText(stats["assistantMessages"])},
		{Label: "工具调用", Value: countText(stats["toolCalls"])},
		{Label: "工具结果", Value: countText(stats["toolResults"])},
		{Label: "消息总数", Value: countText(stats["totalMessages"])},
	})
	data.Token = newStatsSection("Token", tokenFields(stats))
	return r.execute("stats.html", data)
}

func newStatsSection(title string, fields []StatsField) *StatsSection {
	if len(fields) == 0 {
		return nil
	}
	return &StatsSection{Title: title, Fields: fields}
}

// infoSections 给出左栏：会话与项目。
// 会话文件、会话 ID、工作目录、Git 分支这几项是可以直接复制去用的值，
// 因此带上复制按钮（与 Pi Web 的会话弹层一致）。
func infoSections(meta StatsMeta) []StatsSection {
	session := make([]StatsField, 0, 4)
	if meta.Name != "" {
		session = append(session, StatsField{Label: "名称", Value: meta.Name})
	}
	if meta.File != "" {
		session = append(session, StatsField{Label: "会话文件", Value: meta.File, Copy: meta.File, CopyLabel: "会话文件路径"})
	}
	if meta.ID != "" {
		session = append(session, StatsField{Label: "会话 ID", Value: meta.ID, Copy: meta.ID, CopyLabel: "会话 ID"})
	}
	if meta.Status != "" {
		session = append(session, StatsField{Label: "状态", Value: meta.Status})
	}

	project := make([]StatsField, 0, 3)
	if meta.Cwd != "" {
		project = append(project, StatsField{Label: "工作目录", Value: meta.Cwd, Copy: meta.Cwd, CopyLabel: "工作目录"})
	}
	if meta.Branch != "" {
		project = append(project, StatsField{Label: "Git 分支", Value: meta.Branch, Copy: meta.Branch, CopyLabel: "分支名"})
	}
	if meta.Worktree != "" {
		project = append(project, StatsField{Label: "Git 工作树", Value: meta.Worktree, Copy: meta.Worktree, CopyLabel: "工作树路径"})
	}

	runtime := []StatsField{
		{Label: "模型", Value: orDash(meta.Model)},
		{Label: "思考强度", Value: orDash(meta.Thinking)},
		{Label: "工具预设", Value: orDash(meta.Preset)},
	}

	out := make([]StatsSection, 0, 3)
	if len(session) > 0 {
		out = append(out, StatsSection{Title: "会话", Fields: session})
	}
	if len(project) > 0 {
		out = append(out, StatsSection{Title: "项目", Fields: project})
	}
	out = append(out, StatsSection{Title: "运行", Fields: runtime})
	return out
}

// tokenFields 给出右栏：Token 与用量。
// 缓存读写、费用、命中率只在实际发生时出现，不用一串 0 淹没有效信息。
func tokenFields(stats map[string]any) []StatsField {
	tokens := recordOf(stats["tokens"])
	out := make([]StatsField, 0, 8)
	if len(tokens) == 0 {
		return out
	}
	out = append(out,
		StatsField{Label: "输入", Value: countText(tokens["input"])},
		StatsField{Label: "输出", Value: countText(tokens["output"])},
	)
	if numberField(tokens["cacheRead"]) > 0 {
		out = append(out, StatsField{Label: "缓存读取", Value: countText(tokens["cacheRead"])})
	}
	if numberField(tokens["cacheWrite"]) > 0 {
		out = append(out, StatsField{Label: "缓存写入", Value: countText(tokens["cacheWrite"])})
	}
	out = append(out, StatsField{Label: "总计", Value: countText(tokens["total"])})

	if cost := numberField(stats["cost"]); cost > 0 {
		out = append(out, StatsField{Label: "费用", Value: "$" + strconv.FormatFloat(cost, 'f', 4, 64)})
	}
	if usage := recordOf(stats["contextUsage"]); len(usage) > 0 {
		percent := "?"
		if value, ok := usage["percent"].(float64); ok {
			percent = strconv.FormatFloat(value, 'f', 1, 64) + "%"
		}
		out = append(out, StatsField{
			Label: "上下文",
			Value: percent + " / " + compactNumber(numberField(usage["contextWindow"])),
		})
	}
	// 命中率 = 缓存读取 /（输入 + 缓存写入 + 缓存读取），
	// 分母覆盖全部输入类 Token，否则会算出 >100%。
	read, write, input := numberField(tokens["cacheRead"]), numberField(tokens["cacheWrite"]), numberField(tokens["input"])
	if denominator := read + write + input; denominator > 0 && read+write > 0 {
		out = append(out, StatsField{
			Label: "缓存命中率",
			Value: strconv.FormatFloat(read/denominator*100, 'f', 1, 64) + "%",
		})
	}
	return out
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
