package presentation

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"pi-bridge-go/internal/magiccontext"
)

// MCData 驱动 magic-context 面板模板。
type MCData struct {
	Kind   string
	Kinds  []MCKind
	Status magiccontext.Status
	Rows   []MCRow
	// Total 是该分区的总行数；面板要区分「没有数据」与「这一页没有数据」。
	Total int
	// Offset / Limit 是当前页位置，用于渲染「加载更多」。
	Offset int
	Limit  int
	// Category 是当前选中的记忆分类，空表示全部。
	Category   string
	Categories []MCCategory
	// Projects 与选中项同理。
	Project  string
	Projects []MCProject
	// Notice 在库不可用等情况下替代列表显示。
	Notice  string
	MoreURL string
	Append  bool
}

// MCKind 是面板上的一个分区入口。
type MCKind struct {
	Key     string
	Label   string
	Hint    string
	Current bool
}

// MCRow 是列表里的一行。字段按分区不同而不同，模板按名字取。
type MCRow struct {
	ID       string
	Kind     string
	Title    string
	Preview  string
	Category string
	Scope    string
	Import   int
	Status   string
	When     string
	Extra    []MCField
	// FullLen 是正文完整长度；大于预览长度时模板才显示「展开」。
	FullLen int
}

// MCField 是行尾的补充字段，例如「引用 3 次」。
type MCField struct {
	Label string
	Value string
}

// MCCategory / MCProject 是筛选入口。
type MCCategory struct {
	Name    string
	Count   int
	Current bool
}

type MCProject struct {
	Key     string
	Count   int
	Current bool
}

// RenderMCContent 渲染显式展开的正文，读取器负责分区、身份和长度校验。
func (r *Renderer) RenderMCContent(ctx context.Context, kind magiccontext.Kind, id int64) (string, error) {
	content, err := r.mc.Detail(ctx, kind, id)
	if err != nil {
		return "", err
	}
	return r.execute("mc-content.html", content)
}

// RenderMC 渲染只读面板：列表截断正文，展开单独读取；不可用时明确原因。
func (r *Renderer) RenderMC(ctx context.Context, kind magiccontext.Kind, offset, limit int, category, project string, appendRows bool) (string, error) {
	status := r.mc.Status(ctx)
	data := MCData{Append: appendRows, Kind: string(kind), Offset: offset, Limit: limit, Status: status, Category: category, Project: project}
	for _, entry := range magiccontext.Kinds {
		data.Kinds = append(data.Kinds, MCKind{Key: string(entry.Key), Label: entry.Label, Hint: entry.Hint, Current: entry.Key == kind})
	}
	if !status.Available {
		data.Notice = status.Reason
		return r.execute("mc.html", data)
	}
	// 分类与项目筛选只对记忆分区有意义，其它分区没有这两列。
	filter := magiccontext.Filter{}
	if kind == magiccontext.KindMemories {
		filter = magiccontext.Filter{Category: category, Project: project}
	}
	rows, total, err := r.mc.List(ctx, kind, offset, limit, filter)
	if err != nil {
		data.Notice = fmt.Sprintf("读取失败：%s", err)
		return r.execute("mc.html", data)
	}
	data.Total = total
	if offset+len(rows) < total && len(rows) > 0 {
		q := url.Values{"kind": {string(kind)}, "offset": {strconv.Itoa(offset + len(rows))}, "limit": {strconv.Itoa(limit)}, "category": {category}, "project": {project}, "append": {"1"}}
		// 相对路径：云端外壳注入 <base href="/d/{id}/">，根绝对路径会被
		// 解析到 relay 根而不是设备前缀，分页在云端静默失效。
		data.MoreURL = "ui/mc?" + q.Encode()
	}
	for _, row := range rows {
		data.Rows = append(data.Rows, r.mcRow(kind, row))
	}
	// 分类与项目筛选只在记忆分区有意义，其它分区没有这两列。
	if kind == magiccontext.KindMemories {
		if cats, err := r.mc.Categories(ctx); err == nil {
			for _, cat := range cats {
				data.Categories = append(data.Categories, MCCategory{Name: cat.Name, Count: cat.Count, Current: cat.Name == category})
			}
		}
		if projects, err := r.mc.Projects(ctx); err == nil {
			for _, item := range projects {
				data.Projects = append(data.Projects, MCProject{Key: item.Key, Count: item.Count, Current: item.Key == project})
			}
		}
	}
	return r.execute("mc.html", data)
}

// mcRow 把库里的一行转成模板行。
//
// 主标题一律取「这一行是什么内容」，分类只作为标签：
// 曾经把分类同时当标题又当标签，结果每行都是「ARCHITECTURE · ARCHITECTURE」，
// 收起态一条记忆都分不出来。
func (r *Renderer) mcRow(kind magiccontext.Kind, row magiccontext.Row) MCRow {
	out := MCRow{ID: row.ID(), Kind: string(kind)}
	out.Preview = row.Text("preview")
	out.FullLen = row.Int("content_len")
	switch kind {
	case magiccontext.KindMemories:
		out.Title = clip(out.Preview, 90)
		out.Category = row.Text("category")
		out.Scope = row.Text("scope")
		out.Import = row.Int("importance")
		out.Extra = []MCField{
			{Label: "来源", Value: row.Text("source_type")},
			{Label: "见到", Value: strconv.Itoa(row.Int("seen_count"))},
			{Label: "引用", Value: strconv.Itoa(row.Int("retrieval_count"))},
		}
	case magiccontext.KindCompartments:
		out.Title = firstNonEmpty(row.Text("title"), clip(out.Preview, 90))
		out.Extra = []MCField{
			{Label: "会话", Value: shortSession(row.Text("session_id"))},
			{Label: "序号", Value: strconv.Itoa(row.Int("sequence"))},
			{Label: "消息", Value: fmt.Sprintf("%d–%d", row.Int("start_message"), row.Int("end_message"))},
		}
	case magiccontext.KindDirectives:
		out.Title = clip(out.Preview, 90)
	case magiccontext.KindNotes:
		out.Title = clip(out.Preview, 90)
		out.Category = row.Text("type")
		out.Status = row.Text("status")
		out.Extra = []MCField{
			{Label: "触发条件", Value: row.Text("surface_condition")},
		}
	case magiccontext.KindDreams:
		out.Title = "Dreamer 运行"
		out.Extra = []MCField{
			{Label: "成功", Value: strconv.Itoa(row.Int("tasks_succeeded"))},
			{Label: "失败", Value: strconv.Itoa(row.Int("tasks_failed"))},
		}
	}
	if out.Title == "" {
		out.Title = "（无标题）"
	}
	out.When = formatEpochMillis(row.Int("updated_at"))
	if out.When == "" {
		out.When = formatEpochMillis(row.Int("created_at"))
	}
	if out.When == "" {
		out.When = formatEpochMillis(row.Int("started_at"))
	}
	return out
}

// shortSession 把会话 ID 缩成 8 位，行内放得下。
func shortSession(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// formatEpochMillis 把毫秒时间戳格式化成「YYYY-MM-DD HH:MM」。
// 0 与越界值返回空串——库里时间戳可能是 NULL，不能显示成 1970。
func formatEpochMillis(ms int) string {
	if ms <= 0 {
		return ""
	}
	seconds := ms / 1000
	// 2100 年之后视为脏数据，宁可不显示。
	if seconds <= 0 || seconds > 4102444800 {
		return ""
	}
	return time.Unix(int64(seconds), 0).Format("2006-01-02 15:04")
}

// clip 按字符数截断并加省略号。SQLite 的 substr 已按字符截断，
// 这里再收一档是为了让收起态的一行放得下。
func clip(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
