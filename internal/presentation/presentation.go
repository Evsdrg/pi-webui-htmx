// Package presentation 把桥的数据渲染成 HTML 片段，供 htmx 直接换入 DOM。
// 分工：请求-响应类界面走这里（服务器渲染片段），
// 流式对话走 WS + JSON + 少量客户端脚本。
package presentation

import (
	"embed"
	"html/template"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/sessions"
)

//go:embed templates/*.html assets/*
var files embed.FS

// Renderer 持有解析后的模板与静态资源。
// 模板在启动时解析一次，运行期只执行，避免每次请求重新解析。
type Renderer struct {
	templates map[string]*template.Template
	assets    map[string][]byte
	mu        sync.RWMutex
}

// New 解析全部模板并加载静态资源。
func New() (*Renderer, error) {
	entries, err := files.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	r := &Renderer{templates: map[string]*template.Template{}, assets: map[string][]byte{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := files.ReadFile("templates/" + e.Name())
		if err != nil {
			return nil, err
		}
		// FuncMap 提供少量格式化助手；全部输出仍走 html/template 的上下文转义。
		tpl, err := template.New(e.Name()).Funcs(funcMap()).Parse(string(b))
		if err != nil {
			return nil, err
		}
		r.templates[e.Name()] = tpl
	}
	assets, err := files.ReadDir("assets")
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.IsDir() {
			continue
		}
		b, err := files.ReadFile("assets/" + a.Name())
		if err != nil {
			return nil, err
		}
		r.assets[a.Name()] = b
	}
	return r, nil
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"printf": func(format string, args ...any) string {
			// 只用于生成 option 的 value，输出仍会被属性转义。
			var b strings.Builder
			for i := 0; i < len(format); i++ {
				if format[i] == '%' && i+1 < len(format) {
					switch format[i+1] {
					case 's':
						if len(args) > 0 {
							if s, ok := args[0].(string); ok {
								b.WriteString(s)
								args = args[1:]
							}
						}
						i++
						continue
					case '/':
						b.WriteByte('/')
						i++
						continue
					}
				}
				b.WriteByte(format[i])
			}
			return b.String()
		},
	}
}

// Asset 返回静态资源内容与 MIME 类型。
func (r *Renderer) Asset(name string) ([]byte, string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.assets[name]
	if !ok {
		return nil, "", false
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		return b, "text/css; charset=utf-8", true
	case strings.HasSuffix(name, ".js"):
		return b, "text/javascript; charset=utf-8", true
	case strings.HasSuffix(name, ".html"):
		return b, "text/html; charset=utf-8", true
	}
	return b, "application/octet-stream", true
}

// execute 渲染指定模板；渲染失败视为内部错误，不回退部分输出。
func (r *Renderer) execute(name string, data any) (string, error) {
	r.mu.RLock()
	tpl := r.templates[name]
	r.mu.RUnlock()
	if tpl == nil {
		return "", protocol.E("not_found", "模板不存在")
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", protocol.E("pi_error", "渲染失败")
	}
	return b.String(), nil
}

// SessionRow 是侧栏的一行。
type SessionRow struct {
	ID       string
	Title    string
	Modified string
}

// SessionsData 驱动侧栏模板。
type SessionsData struct {
	Items      []SessionRow
	Selected   string
	HasMore    bool
	NextOffset int
}

// RenderSessions 渲染侧栏会话列表。
func (r *Renderer) RenderSessions(list sessions.Listing, selected string) (string, error) {
	items := make([]SessionRow, 0, len(list.Items))
	for _, h := range list.Items {
		items = append(items, SessionRow{
			ID:       h.ID,
			Title:    sessionTitle(h),
			Modified: h.Modified.Local().Format("01-02 15:04"),
		})
	}
	return r.execute("sessions.html", SessionsData{
		Items: items, Selected: selected, HasMore: list.HasMore,
		NextOffset: len(list.Items),
	})
}

// sessionTitle 取会话显示名，缺失时退回 ID 前缀。
func sessionTitle(h sessions.Header) string {
	if h.Name != "" {
		return h.Name
	}
	if len(h.ID) > 8 {
		return h.ID[:8]
	}
	return h.ID
}

// Step 是回合内的一个过程步骤。
type Step struct {
	Kind   string
	Detail string
}

// Turn 是一个完整回合：用户消息 + 过程 + 助手回复。
// 整轮渲染是刻意的：Pi Web 按单条消息切片，往回翻页时
// 会把已在屏幕上的 assistant 重新折进 ProcessDetailsGroup，
// 视口内容被顶走。一个片段只含完整回合，插入位置永远在轮边界。
type Turn struct {
	ID            string
	UserText      string
	AssistantText string
	Steps         []Step
	HasProcess    bool
}

// HistoryData 驱动历史模板。
type HistoryData struct {
	SessionID     string
	LeafID        string
	Turns         []Turn
	HasMore       bool
	OldestEntryID string
}

// GroupTurns 把分支条目聚合成完整回合。
// 规则：一条 user 消息开启新回合；工具调用归入当前回合的 Steps；
// 没有 user 锚点的孤儿 assistant 单独成轮，不并入上一轮，
// 否则往上翻页时它会被重新折叠，造成视口跳动。
func GroupTurns(entries []sessions.Entry) []Turn {
	turns := []Turn{}
	current := -1
	for _, e := range entries {
		switch e.Kind {
		case sessions.KindUser:
			turns = append(turns, Turn{ID: e.ID, UserText: e.Text})
			current = len(turns) - 1
		case sessions.KindAssistant:
			if current < 0 {
				turns = append(turns, Turn{ID: e.ID, AssistantText: e.Text})
				continue
			}
			turns[current].AssistantText += e.Text
		case sessions.KindTool:
			if current < 0 {
				turns = append(turns, Turn{ID: e.ID, Steps: []Step{{Kind: "工具", Detail: e.Text}}})
				continue
			}
			turns[current].Steps = append(turns[current].Steps, Step{Kind: "工具", Detail: e.Text})
			turns[current].HasProcess = true
		case sessions.KindCompaction:
			// 压缩边界单独成轮，避免把摘要并进相邻回合。
			turns = append(turns, Turn{ID: e.ID, AssistantText: e.Text})
			current = len(turns) - 1
		}
	}
	return turns
}

// RenderHistory 渲染一页历史片段。
func (r *Renderer) RenderHistory(sessionID string, page sessions.Page) (string, error) {
	turns := GroupTurns(sessions.ProjectEntries(page.Entries))
	return r.execute("history.html", HistoryData{
		SessionID:     sessionID,
		LeafID:        page.LeafID,
		Turns:         turns,
		HasMore:       page.HasMore,
		OldestEntryID: page.OldestEntryID,
	})
}

// ModelRow 是模型选择器的一行。
type ModelRow struct {
	ID       string
	Name     string
	Provider string
}

// ModelsData 驱动模型选择器。
type ModelsData struct {
	Models  []ModelRow
	Current string
}

// RenderModels 渲染模型下拉框。
func (r *Renderer) RenderModels(models []map[string]any, current string) (string, error) {
	rows := make([]ModelRow, 0, len(models))
	for _, m := range models {
		row := ModelRow{
			ID:       stringField(m, "id"),
			Name:     stringField(m, "name"),
			Provider: stringField(m, "provider"),
		}
		if row.ID == "" {
			continue
		}
		if row.Name == "" {
			row.Name = row.ID
		}
		rows = append(rows, row)
	}
	return r.execute("models.html", ModelsData{Models: rows, Current: current})
}

// PackageRow 是资源清单的一行。
type PackageRow struct {
	Name      string
	Source    string
	Version   string
	Latest    string
	HasUpdate bool
	Disabled  bool
	Error     string
}

// PackagesData 驱动资源清单。
type PackagesData struct {
	Packages []PackageRow
}

// RenderPackages 渲染已安装资源清单。
func (r *Renderer) RenderPackages(packages []map[string]any) (string, error) {
	rows := make([]PackageRow, 0, len(packages))
	for _, p := range packages {
		rows = append(rows, PackageRow{
			Name:      stringField(p, "name"),
			Source:    stringField(p, "source"),
			Version:   stringField(p, "version"),
			Latest:    stringField(p, "latest"),
			HasUpdate: boolField(p, "hasUpdate"),
			Disabled:  boolField(p, "disabled"),
			Error:     stringField(p, "error"),
		})
	}
	return r.execute("packages.html", PackagesData{Packages: rows})
}

// FileRow 是文件浏览的一行。
type FileRow struct {
	Name  string
	Path  string
	IsDir bool
	Size  string
}

// FilesData 驱动文件浏览。
type FilesData struct {
	Root      string
	Entries   []FileRow
	Truncated bool
}

// RenderFiles 渲染文件浏览片段。
func (r *Renderer) RenderFiles(root string, entries []map[string]any, truncated bool) (string, error) {
	rows := make([]FileRow, 0, len(entries))
	for _, e := range entries {
		row := FileRow{
			Name:  stringField(e, "name"),
			Path:  stringField(e, "path"),
			IsDir: boolField(e, "isDir"),
			Size:  formatSize(intField(e, "size")),
		}
		if row.Name == "" {
			continue
		}
		rows = append(rows, row)
	}
	return r.execute("files.html", FilesData{Root: root, Entries: rows, Truncated: truncated})
}

// RenderShell 渲染应用外壳。
func (r *Renderer) RenderShell(sessionID string) (string, error) {
	return r.execute("shell.html", map[string]string{"SessionID": sessionID})
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// formatSize 把字节数格式化成短字符串。
func formatSize(n int) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return itoa(n/int(div)) + " " + string("KMGTPE"[exp]) + "iB"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Now 供测试替换时间来源。
var Now = time.Now
