// Package presentation 把桥的数据渲染成 HTML 片段，供 htmx 直接换入 DOM。
//
// 分工：请求-响应类界面走这里（服务器渲染片段），
// 流式对话走 WS + JSON + 少量客户端脚本。
//
// 模板与静态资源的唯一归属是 pi-webui-htmx 仓；桥不内嵌副本，
// 从 --ui-dir 加载。UI 改样子不需要动桥。
package presentation

import (
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/sessions"
)

// Renderer 持有解析后的模板与构建产物。
// 模板在启动时解析一次，运行期只执行。
type Renderer struct {
	templates map[string]*template.Template
	assets    map[string]asset
	entryJS   []string
	entryCSS  []string
	mu        sync.RWMutex
}

// asset 是一份静态资源。
type asset struct {
	path string
	mime string
}

// LoadFromDir 从 UI 包目录加载。
//
// dir 指向 pi-webui-htmx 检出：
//   - 模板取 src/templates（htmx 片段，Go html/template 语法）
//   - 静态资源取 dist/assets（Vite 构建产物，文件名带内容哈希）
//
// 拒绝在缺模板或缺构建产物时启动——不带半套 UI 跑。
func LoadFromDir(dir string, supported ...string) (*Renderer, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, protocol.E("invalid_params", "未指定 UI 包目录")
	}
	templatesDir := filepath.Join(dir, "src", "templates")
	assetsDir := filepath.Join(dir, "dist", "assets")
	if _, err := os.Stat(templatesDir); err != nil {
		return nil, protocol.E("not_found", "UI 包缺少 src/templates")
	}
	if _, err := os.Stat(assetsDir); err != nil {
		return nil, protocol.E("not_found", "UI 包缺少 dist/assets，请先执行 pnpm build")
	}

	r := &Renderer{templates: map[string]*template.Template{}, assets: map[string]asset{}}
	if err := r.loadTemplates(templatesDir); err != nil {
		return nil, err
	}
	if err := r.loadAssets(assetsDir); err != nil {
		return nil, err
	}
	if err := r.loadManifest(dir, supported); err != nil {
		return nil, err
	}
	return r, nil
}

// loadTemplates 解析目录下全部 .html，子目录展平为「目录/文件名」。
func (r *Renderer) loadTemplates(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return protocol.E("pi_error", "读取模板目录失败")
	}
	for _, e := range entries {
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			for _, f := range sub {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".html") {
					continue
				}
				name := e.Name() + "/" + f.Name()
				if err := r.parseTemplate(filepath.Join(root, name), name); err != nil {
					return err
				}
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		if err := r.parseTemplate(filepath.Join(root, e.Name()), e.Name()); err != nil {
			return err
		}
	}
	if len(r.templates) == 0 {
		return protocol.E("not_found", "UI 包没有任何模板")
	}
	return nil
}

func (r *Renderer) parseTemplate(path, name string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return protocol.E("pi_error", "读取模板失败: "+name)
	}
	tpl, err := template.New(name).Funcs(funcMap()).Parse(string(b))
	if err != nil {
		return protocol.E("pi_error", "解析模板失败: "+name)
	}
	r.templates[name] = tpl
	return nil
}

// loadAssets 加载 Vite 构建产物。
func (r *Renderer) loadAssets(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return protocol.E("pi_error", "读取构建产物失败")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return protocol.E("pi_error", "构建产物不是受限普通文件: "+e.Name())
		}
		r.assets[e.Name()] = asset{path: filepath.Join(dir, e.Name()), mime: mimeFor(e.Name())}
	}
	if len(r.assets) == 0 {
		return protocol.E("not_found", "UI 包构建产物为空")
	}
	return nil
}

// mimeFor 按扩展名返回 MIME。
func mimeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	}
	return "application/octet-stream"
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"printf": func(format string, args ...any) string {
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

// Asset 按真实文件名返回静态资源。缓存一天：文件名带内容哈希，内容不会变。
func (r *Renderer) Asset(name string) (body []byte, mime string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 只接受单层文件名，拒绝路径穿越。
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, "", false
	}
	a, exists := r.assets[name]
	if !exists {
		return nil, "", false
	}
	// 资源按请求读取，由操作系统文件缓存复用，桥不常驻全部惰性库。
	b, err := os.ReadFile(a.path)
	if err != nil {
		return nil, "", false
	}
	return b, a.mime, true
}

// EntryAssets 返回入口的 JS 与 CSS 真实文件名。
// shell 模板用它注入带内容哈希的路径，从而支持长期不可变缓存。
func (r *Renderer) EntryAssets() (js, css []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.entryJS...), append([]string(nil), r.entryCSS...)
}

// execute 渲染指定模板；失败视为内部错误，不回退部分输出。
func (r *Renderer) execute(name string, data any) (string, error) {
	r.mu.RLock()
	tpl := r.templates[name]
	r.mu.RUnlock()
	if tpl == nil {
		return "", protocol.E("not_found", "模板不存在: "+name)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", protocol.E("pi_error", "渲染失败: "+name)
	}
	return b.String(), nil
}

// TemplateNames 返回已加载的模板名，供启动日志与测试使用。
func (r *Renderer) TemplateNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.templates))
	for name := range r.templates {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// SessionRow 是侧栏的一行。
type SessionRow struct {
	ID       string
	Title    string
	Modified string
	Cwd      string
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
	return r.RenderSessionsPage(list, selected, 0)
}

// RenderSessionsPage 保留翻页偏移，避免第二页之后重复加载同一页。
func (r *Renderer) RenderSessionsPage(list sessions.Listing, selected string, offset int) (string, error) {
	items := make([]SessionRow, 0, len(list.Items))
	for _, h := range list.Items {
		items = append(items, SessionRow{
			ID:       h.ID,
			Cwd:      h.Cwd,
			Title:    sessionTitle(h),
			Modified: h.Modified.Local().Format("01-02 15:04"),
		})
	}
	return r.execute("sessions.html", SessionsData{
		Items: items, Selected: selected, HasMore: list.HasMore,
		NextOffset: offset + len(list.Items),
	})
}

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
			if turns[current].AssistantText != "" && e.Text != "" {
				turns[current].AssistantText += "\n\n"
			}
			turns[current].AssistantText += e.Text
		case sessions.KindTool:
			if current < 0 {
				turns = append(turns, Turn{ID: e.ID, HasProcess: true, Steps: []Step{{Kind: "工具", Detail: e.Text}}})
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
	return r.execute("history.html", HistoryData{
		SessionID:     sessionID,
		LeafID:        page.LeafID,
		Turns:         GroupTurns(sessions.ProjectEntries(page.Entries)),
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

// DiffLine 是 diff 的一行。
type DiffLine struct {
	Kind  string
	OldNo int
	NewNo int
	Text  string
}

// DiffFile 是一个文件的 diff。
type DiffFile struct {
	Path      string
	IsNew     bool
	IsDeleted bool
	IsBinary  bool
	Lines     []DiffLine
}

// DiffData 驱动 diff 模板。diff 由服务端渲染，不引 diff2html。
type DiffData struct {
	SessionID string
	Files     []DiffFile
}

// RenderDiff 渲染 diff 片段。
func (r *Renderer) RenderDiff(sessionID string, files []DiffFile) (string, error) {
	return r.execute("diff.html", DiffData{SessionID: sessionID, Files: files})
}

// ShellData 驱动应用外壳。
type ShellData struct {
	SessionID string
	JS        []string
	CSS       []string
}

// RenderShell 渲染应用外壳，注入带内容哈希的资源路径。
func (r *Renderer) RenderShell(sessionID string) (string, error) {
	js, css := r.EntryAssets()
	return r.execute("shell.html", ShellData{SessionID: sessionID, JS: js, CSS: css})
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
	div, exp := 1024, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return itoa(n/div) + " " + string("KMGTPE"[exp]) + "iB"
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
