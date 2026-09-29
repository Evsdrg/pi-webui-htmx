// Package presentation 把桥的数据渲染成 HTML 片段，供 htmx 直接换入 DOM。
//
// 分工：请求-响应类界面走这里（服务器渲染片段），
// 流式对话走 WS + JSON + 少量客户端脚本。
//
// 模板与静态资源的唯一归属是 pi-webui-htmx 仓；桥不内嵌副本，
// 从 --ui-dir 加载。UI 改样子不需要动桥。
package presentation

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/magiccontext"
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
	// entryPreload 是入口的静态分块（如拆出去的 htmx）。它们不在
	// 入口请求里，如果不预加载，浏览器要解析完 app.js 才发现它们，
	// 多一个往返才能拿到 window.htmx。
	entryPreload []string
	mu           sync.RWMutex
	// compressed 缓存静态资源的预压缩变体。键是 "name|enc"。
	// 文件名带内容哈希，内容不会变，因此缓存永不失效；上限只为防止
	// 有人把 --ui-dir 指向巨型目录时无界增长。
	compressed   map[string][]byte
	compressedAt int
	// mc 是 magic-context 本地存储的只读视图。它为 nil 时面板不可用，
	// 由 SetMagicContext 注入——构造 Renderer 时还没有传输层的信息。
	mc *magiccontext.Store
}

// asset 是一份静态资源。
type asset struct {
	path string
	mime string
}

// 压缩缓存的硬上限。dist/ 通常约 2.4 MB 原文、压缩后约 600 KB，
// 这个上限足够装下全部产物又不会无限增长。
const (
	maxCompressedEntries = 1024
	maxCompressedBytes   = 64 << 20
)

// SetMagicContext 注入 magic-context 只读视图。
// 渲染器可以为 nil（没配 --ui-dir 时 UI 层整体禁用），所以这里判空——
// 测试与禁用 UI 的部署都会走到这条路径。
func (r *Renderer) SetMagicContext(store *magiccontext.Store) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.mc = store
	r.mu.Unlock()
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

	r := &Renderer{templates: map[string]*template.Template{}, assets: map[string]asset{}, compressed: map[string][]byte{}}
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

// Encodings 是桥支持的响应编码，按客户端偏好从高到低排列。
// gzip 用标准库；brotli 压缩率再高约 11%，静态资产值得多这一个依赖。
var Encodings = []string{"br", "gzip"}

// PickEncoding 按 Accept-Encoding 选编码；客户端不支持时返回空字符串。
//
// 解析 qvalue：`br;q=0, gzip;q=1` 表示客户端明确禁用 br，只能选 gzip。
// 省略 q 视为 1；未列出且无 `*` 视为不可接受。同分时按 Encodings 顺序
// （br 压缩率更高）取先者。
func PickEncoding(accept string) string {
	if accept == "" {
		return ""
	}
	// 头部来自客户端，限制长度避免异常输入下的无谓解析。
	if len(accept) > 1024 {
		accept = accept[:1024]
	}
	weights := map[string]float64{}
	star := -1.0
	for _, field := range strings.Split(accept, ",") {
		name, q, ok := parseAcceptField(field)
		if !ok {
			continue
		}
		if name == "*" {
			star = q
			continue
		}
		// 同名重复出现时取最严格的（最小权重），避免用后面的项覆盖明确禁用。
		if prev, exists := weights[name]; exists && prev <= q {
			continue
		}
		weights[name] = q
	}
	best, bestQ := "", 0.0
	for _, name := range Encodings {
		q, explicit := weights[name]
		if !explicit {
			if star < 0 {
				continue
			}
			q = star
		}
		if q <= 0 {
			continue
		}
		if q > bestQ {
			best, bestQ = name, q
		}
	}
	return best
}

// parseAcceptField 解析 `name;q=0.5` 一项，返回规范化名称与权重。
func parseAcceptField(field string) (string, float64, bool) {
	field = strings.TrimSpace(field)
	if field == "" {
		return "", 0, false
	}
	name, param, _ := strings.Cut(field, ";")
	name = strings.ToLower(strings.TrimSpace(name))
	// 只接受 token 字符，拒绝畸形项而不是猜测其含义。
	if name == "" || len(name) > 32 {
		return "", 0, false
	}
	for _, ch := range name {
		if !isTokenChar(ch) {
			return "", 0, false
		}
	}
	q := 1.0
	if param != "" {
		key, value, found := strings.Cut(strings.TrimSpace(param), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "q") {
			// 无法识别的参数不影响可用性，按默认权重处理。
			return name, q, true
		}
		parsed, ok := parseQValue(strings.TrimSpace(value))
		if !ok {
			// 非法 q 视为明确禁用：宁可退回 identity 也不发客户端拒绝的编码。
			return name, 0, true
		}
		q = parsed
	}
	return name, q, true
}

// parseQValue 解析 0 到 1 之间、最多三位小数的权重。
func parseQValue(value string) (float64, bool) {
	if len(value) > 5 {
		return 0, false
	}
	dot := strings.IndexByte(value, '.')
	if dot >= 0 && len(value)-dot-1 > 3 {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || parsed > 1 {
		return 0, false
	}
	return parsed, true
}

func isTokenChar(ch rune) bool {
	return ch > 0x20 && ch < 0x7f && !strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", ch)
}

// Asset 按真实文件名返回静态资源，并按 encoding 返回预压缩变体。
// encoding 为空时返回原文。
func (r *Renderer) Asset(name, encoding string) (body []byte, mime string, ok bool) {
	r.mu.RLock()
	a, exists := r.assets[name]
	r.mu.RUnlock()
	// 只接受单层文件名，拒绝路径穿越。
	if !exists || name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return nil, "", false
	}
	// 资源按请求读取，由操作系统文件缓存复用，桥不常驻全部原文。
	// 压缩命中时必须先返回缓存：否则每次请求仍会读完整原文并分配，
	// 缓存只省下 CPU，省不掉这次 IO 与分配。
	if encoding != "" {
		if cached, found := r.cachedAsset(name, encoding); found {
			return cached, a.mime, true
		}
	}
	raw, err := os.ReadFile(a.path)
	if err != nil {
		return nil, "", false
	}
	if encoding == "" {
		return raw, a.mime, true
	}
	compressed, err := compressBytes(raw, encoding)
	if err != nil {
		// 压缩失败不该让资源不可用：退回原文，客户端照常能解析。
		return raw, a.mime, true
	}
	r.storeAsset(name, encoding, compressed)
	return compressed, a.mime, true
}

// cachedAsset 取预压缩变体。
func (r *Renderer) cachedAsset(name, encoding string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.compressed[name+"|"+encoding]
	return b, ok
}

// storeAsset 写入预压缩变体；超过上限时整体清空重来，
// 而不是逐条淘汰——逐条淘汰需要 LRU 簿记，成本高于收益。
func (r *Renderer) storeAsset(name, encoding string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.compressedAt+len(body) > maxCompressedBytes || len(r.compressed) >= maxCompressedEntries {
		r.compressed = map[string][]byte{}
		r.compressedAt = 0
	}
	r.compressed[name+"|"+encoding] = body
	r.compressedAt += len(body)
}

// CompressedStats 返回压缩缓存规模，用于诊断。
func (r *Renderer) CompressedStats() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return map[string]any{"entries": len(r.compressed), "bytes": r.compressedAt}
}

// EntryAssets 返回入口的 JS 与 CSS 真实文件名。
// shell 模板用它注入带内容哈希的路径，从而支持长期不可变缓存。
func (r *Renderer) EntryAssets() (js, css, preload []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.entryJS...), append([]string(nil), r.entryCSS...), append([]string(nil), r.entryPreload...)
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
	// Name 是工具名（read/bash/edit/...），直接来自工具结果的 toolName 字段。
	// 空值表示记录里没有工具名，模板退回 Kind。
	Name string
	// OK 表示工具结果没有报错。false 时用错误配色，与 Pi Web 的 isError 同义。
	OK bool
	// Duration 是工具执行的整秒数，0 表示不显示。由工具结果时间戳减去
	// 所属 assistant 条目的时间戳得到——assistant 写入即生成结束、工具开始跑，
	// 这与 Pi Web 的 toolCallDurations 推导方式一致。
	Duration int
	// EntryID 与 Images 配对：前者是承载图片块的条目 ID，
	// 后者是块下标。缺任一条件就不渲染占位符。
	EntryID string
	// Images 是这一步里可延后加载的图片块下标；
	// 详情只放文字，base64 图片等用户点了才取。
	Images []int
}

// Turn 是一个完整回合：用户消息 + 过程 + 助手回复。
// 整轮渲染是刻意的：Pi Web 按单条消息切片，往回翻页时
// 会把已在屏幕上的 assistant 重新折进 ProcessDetailsGroup，
// 视口内容被顶走。一个片段只含完整回合，插入位置永远在轮边界。
type Turn struct {
	ID            string
	EntryIDs      []string
	UserText      string
	UserImages    []ImageBlock
	AssistantText string
	Error         string
	Steps         []Step
	HasProcess    bool
	// Thinking 是思考占位符列表，每项自带 entry ID 与块下标。
	// 曾经用「单个 AssistantEntryID + 合并下标」表示，于是一个回合里
	// 多个 assistant 条目时，较早条目承载的块会按最后一个条目的 ID 去取，
	// 既取不回原文，又可能重复出现同一段（B11）。占位符必须自己知道归属。
	Thinking []ThinkingBlock
	// Usage 汇总本回合全部 assistant 条目的 token 与费用。
	// nil 表示这一回合没有任何用量记录（例如压缩边界轮）。
	Usage *sessions.Usage
}

// ImageBlock 指向用户条目中的图片块，历史片段不包含 base64 正文。
type ImageBlock struct {
	EntryID    string
	BlockIndex int
}

// ThinkingBlock 是一个思考占位符：定位到具体条目的具体块。
type ThinkingBlock struct {
	EntryID    string `json:"entryId"`
	BlockIndex int    `json:"blockIndex"`
	// Duration 是承载本块的 assistant 条目的生成整秒数，0 表示不显示。
	// 按块而不是按回合归属：一个回合可能有多条 assistant 条目（每次工具
	// 调用后都会再生成一条），各自有不同的生成耗时。
	Duration int `json:"duration"`
}

// HistoryData 驱动历史模板。
type HistoryData struct {
	SessionID       string
	LeafID          string
	Turns           []Turn
	HasMore         bool
	OldestEntryID   string
	HistoricalModel *sessions.ModelRef
}

// thinkingBlocks 把某个条目的思考块转成自带归属的占位符列表。
func thinkingBlocks(entryID string, blocks []sessions.LazyBlock, duration int) []ThinkingBlock {
	out := []ThinkingBlock{}
	for _, b := range blocks {
		if b.Kind == "thinking" {
			out = append(out, ThinkingBlock{EntryID: entryID, BlockIndex: b.BlockIndex, Duration: duration})
		}
	}
	return out
}

// lazyIndexes 从惰性块列表里挑出某一类的块下标。
func lazyIndexes(blocks []sessions.LazyBlock, kind string) []int {
	var out []int
	for _, b := range blocks {
		if b.Kind == kind {
			out = append(out, b.BlockIndex)
		}
	}
	return out
}

// GroupTurns 把分支条目聚合成完整回合。
// 没有 user 锚点的孤儿 assistant 单独成轮，不并入上一轮，
// 否则往上翻页时它会被重新折叠，造成视口跳动。
func GroupTurns(entries []sessions.Entry) []Turn {
	turns := []Turn{}
	current := -1
	// previous 是上一条目的时间戳，用于推导思考时长；
	// assistantAt 是本回合最后一个 assistant 条目的时间戳，用于推导工具时长。
	var previous, assistantAt time.Time
	for _, e := range entries {
		switch e.Kind {
		case sessions.KindUser:
			images := make([]ImageBlock, 0, len(e.Lazy))
			for _, index := range lazyIndexes(e.Lazy, "image") {
				images = append(images, ImageBlock{EntryID: e.ID, BlockIndex: index})
			}
			turns = append(turns, Turn{ID: e.ID, EntryIDs: []string{e.ID}, UserText: e.Text, UserImages: images})
			current = len(turns) - 1
		case sessions.KindAssistant:
			// 每个块都带上自己的 entry ID，绝不合并到回合级的单一 ID 上。
			// 生成耗时按块带上：本条目的时间戳减前一条目的时间戳。
			thinking := thinkingBlocks(e.ID, e.Lazy, secondsBetween(previous, e.Timestamp))
			assistantAt = e.Timestamp
			if current < 0 {
				turns = append(turns, Turn{ID: e.ID, EntryIDs: []string{e.ID}, AssistantText: e.Text, Error: e.Error, Thinking: thinking, Usage: cloneUsage(e.Usage)})
				continue
			}
			turns[current].EntryIDs = append(turns[current].EntryIDs, e.ID)
			turns[current].Thinking = append(turns[current].Thinking, thinking...)
			if e.Error != "" {
				turns[current].Error = e.Error
			} else if e.Text != "" {
				turns[current].Error = ""
			}
			if turns[current].AssistantText != "" && e.Text != "" {
				turns[current].AssistantText += "\n\n"
			}
			turns[current].AssistantText += e.Text
			if e.Usage != nil {
				if turns[current].Usage == nil {
					copy := *e.Usage
					turns[current].Usage = &copy
				} else {
					turns[current].Usage.Input += e.Usage.Input
					turns[current].Usage.Output += e.Usage.Output
					turns[current].Usage.CacheRead += e.Usage.CacheRead
					turns[current].Usage.CacheWrite += e.Usage.CacheWrite
					turns[current].Usage.Cost += e.Usage.Cost
				}
			}
		case sessions.KindTool:
			images := lazyIndexes(e.Lazy, "image")
			step := Step{Kind: "工具", Detail: e.Text, EntryID: e.ID, Images: images, Name: e.ToolName, OK: !e.Failed, Duration: secondsBetween(assistantAt, e.Timestamp)}
			if current < 0 {
				turns = append(turns, Turn{ID: e.ID, EntryIDs: []string{e.ID}, HasProcess: true, Steps: []Step{step}})
				previous = e.Timestamp
				continue
			}
			turns[current].EntryIDs = append(turns[current].EntryIDs, e.ID)
			turns[current].Steps = append(turns[current].Steps, step)
			turns[current].HasProcess = true
		case sessions.KindCompaction:
			// 压缩边界单独成轮，避免把摘要并进相邻回合。
			turns = append(turns, Turn{ID: e.ID, EntryIDs: []string{e.ID}, AssistantText: e.Text})
			current = len(turns) - 1
		}
		if !e.Timestamp.IsZero() {
			previous = e.Timestamp
		}
	}
	return turns
}

// secondsBetween 返回 to 相对于 from 的整秒数；顺序写反会得到负数，
// 而 ElapsedSeconds 对非正值返回 0，于是时长静默消失。
// 两个调用点都按「后发生的时间戳在前」书写。
func secondsBetween(from, to time.Time) int { return sessions.ElapsedSeconds(from, to) }

// RenderHistory 渲染一页历史片段。
func (r *Renderer) RenderHistory(sessionID string, page sessions.Page) (string, error) {
	return r.execute("history.html", HistoryData{
		SessionID:       sessionID,
		LeafID:          page.LeafID,
		Turns:           GroupTurns(sessions.ProjectEntries(page.Entries)),
		HasMore:         page.HasMore,
		OldestEntryID:   page.OldestEntryID,
		HistoricalModel: page.HistoricalModel,
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

// DirRow 是目录选择器里的一行。
type DirRow struct {
	Name string
	Path string
}

// DirsData 驱动「新建会话」的目录选择器。
//
// Parent 为空表示当前目录就是某个工作区根，前端据此禁用「上一级」——
// 桥只允许在根内浏览，越出根没有意义也不安全。
type DirsData struct {
	Path      string
	Parent    string
	Dirs      []DirRow
	Truncated bool
}

// RenderDirs 渲染目录选择器的子目录列表。
func (r *Renderer) RenderDirs(path, parent string, dirs []map[string]string, truncated bool) (string, error) {
	rows := make([]DirRow, 0, len(dirs))
	for _, d := range dirs {
		row := DirRow{Name: d["name"], Path: d["path"]}
		if row.Name == "" || row.Path == "" {
			continue
		}
		rows = append(rows, row)
	}
	return r.execute("dirs.html", DirsData{Path: path, Parent: parent, Dirs: rows, Truncated: truncated})
}

// GitFileRow 是「变更」列表的一行。
type GitFileRow struct {
	Status string
	Path   string
}

// GitStatusData 驱动 Git 变更列表。
//
// 这些内容以前在浏览器里用 createElement 拼装，但「状态数据 → HTML」
// 本来就该由服务端渲染：前端只负责触发刷新，不再掌握列表结构。
type GitStatusData struct {
	Branch    string
	Clean     bool
	Truncated bool
	Shown     int
	Files     []GitFileRow
}

// RenderGitStatus 渲染 Git 变更片段。
func (r *Renderer) RenderGitStatus(status map[string]any) (string, error) {
	rows := make([]GitFileRow, 0)
	for _, item := range anyList(status["files"]) {
		entry := recordOf(item)
		rows = append(rows, GitFileRow{Status: stringField(entry, "status"), Path: stringField(entry, "path")})
	}
	return r.execute("git-status.html", GitStatusData{
		Branch:    stringField(status, "branch"),
		Clean:     boolField(status, "clean"),
		Truncated: boolField(status, "truncated"),
		Shown:     len(rows),
		Files:     rows,
	})
}

// SearchHit 是一次全文搜索命中。
type SearchHit struct {
	SessionID string
	EntryID   string
	Title     string
	Cwd       string
	Snippet   string
}

// SearchData 驱动搜索结果列表。
type SearchData struct {
	Query   string
	Results []SearchHit
}

// RenderSearch 渲染搜索结果。命中片段本身就是数据到标记的映射，
// 放在前端拼 DOM 既重复又不安全。
func (r *Renderer) RenderSearch(query string, hits []map[string]any) (string, error) {
	rows := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		rows = append(rows, SearchHit{
			SessionID: stringField(hit, "sessionId"),
			EntryID:   stringField(hit, "entryId"),
			Title:     stringField(hit, "title"),
			Cwd:       stringField(hit, "cwd"),
			Snippet:   stringField(hit, "snippet"),
		})
	}
	return r.execute("search.html", SearchData{Query: query, Results: rows})
}

// BranchRow 是分支树的一行。
//
// Level 是 1 起算的层级，同时用于 aria-level 与视觉缩进。
// 缩进在 branchMaxLevel 之后封顶：长会话可能是上万层的线性链，
// 每层都加内边距会把内容挤出屏幕，而且缩进本身也失去辨别意义。
type BranchRow struct {
	Kind    string
	Summary string
	EntryID string
	Level   int
	Current bool
}

// branchMaxLevel 是视觉缩进的上限（含）。层级本身仍如实上报给辅助技术。
const branchMaxLevel = 11

// BranchFork 是可分支的用户消息。
type BranchFork struct {
	EntryID string
	Text    string
}

// BranchData 驱动分支导航片段。
type BranchData struct {
	Rows  []BranchRow
	Forks []BranchFork
}

// RenderBranch 渲染分支树与可分支消息列表。
func (r *Renderer) RenderBranch(rows []BranchRow, forks []BranchFork) (string, error) {
	return r.execute("branch.html", BranchData{Rows: rows, Forks: forks})
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
	// Preload 是入口静态依赖的分块，用 modulepreload 与入口并行拉取。
	Preload []string
}

// RenderShell 渲染应用外壳，注入带内容哈希的资源路径。
func (r *Renderer) RenderShell(sessionID string) (string, error) {
	js, css, preload := r.EntryAssets()
	return r.execute("shell.html", ShellData{SessionID: sessionID, JS: js, CSS: css, Preload: preload})
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// recordOf 把任意值当只读对象看待；取不到就给空对象，调用方无需逐层断言。
func recordOf(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// anyList 把 []any 与 []map[string]any 统一成可遍历的切片。
func anyList(value any) []any {
	switch list := value.(type) {
	case []any:
		return list
	case []map[string]string:
		out := make([]any, 0, len(list))
		for _, item := range list {
			m := map[string]any{}
			for k, v := range item {
				m[k] = v
			}
			out = append(out, m)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(list))
		for _, item := range list {
			out = append(out, item)
		}
		return out
	}
	return nil
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
		return strconv.Itoa(n) + " B"
	}
	div, exp := 1024, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.Itoa(n/div) + " " + string("KMGTPE"[exp]) + "iB"
}

// BranchRows 把 Pi 的会话树与可分支消息转成渲染行。
//
// 摊平必须用显式栈而不是递归：长会话的分支树可能是上万层的线性链，
// 递归会直接栈溢出（U08）。这里与前端原先的实现保持同样的前序顺序。
func BranchRows(tree, forks map[string]any, leafId string) ([]BranchRow, []BranchFork) {
	type frame struct {
		node  map[string]any
		depth int
	}
	roots := anyList(tree["tree"])
	stack := make([]frame, 0, len(roots))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, frame{node: recordOf(roots[i])})
	}
	rows := make([]BranchRow, 0, len(roots))
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entry := recordOf(item.node["entry"])
		id := stringField(entry, "id")
		kind := stringField(entry, "type")
		if kind == "" {
			kind = "entry"
		}
		children := anyList(item.node["children"])
		if len(children) > 1 {
			kind = fmt.Sprintf("%s ⑂%d", kind, len(children))
		}
		summary := stringField(item.node, "label")
		if summary == "" {
			summary = stringField(entry, "summary")
		}
		if summary == "" {
			summary = stringField(entry, "text")
		}
		if summary == "" {
			summary = stringField(entry, "name")
		}
		if summary == "" {
			if role := stringField(recordOf(entry["message"]), "role"); role != "" {
				summary = role + " 消息"
			}
		}
		if summary != "" && id != "" {
			summary = shortID(id) + " · " + summary
		} else if summary == "" {
			summary = shortID(id)
		}
		level := item.depth + 1
		if level > branchMaxLevel {
			level = branchMaxLevel
		}
		rows = append(rows, BranchRow{
			Kind:    kind,
			Summary: summary,
			EntryID: id,
			Level:   level,
			Current: id != "" && id == leafId,
		})
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, frame{node: recordOf(children[i]), depth: item.depth + 1})
		}
	}

	// fork_messages 的形态随桥版本不同：这里同时接受裸数组与 {messages:[...]}。
	messages := anyList(forks["messages"])
	if messages == nil {
		messages = anyList(forks)
	}
	forkRows := make([]BranchFork, 0, len(messages))
	for _, item := range messages {
		if len(forkRows) >= 200 {
			break
		}
		entry := recordOf(item)
		id := stringField(entry, "entryId")
		body := stringField(entry, "text")
		if body == "" {
			body = id
		}
		forkRows = append(forkRows, BranchFork{EntryID: id, Text: body})
	}
	return rows, forkRows
}

// shortID 把条目 ID 截成前 8 位，界面上只用于区分节点。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// NoteData 驱动一条纯文本提示片段。
type NoteData struct {
	Message string
}

// RenderNote 渲染一条说明片段。
//
// 用途：片段端点服务于 htmx，而 htmx 默认不交换 4xx/5xx 响应，
// 因此「还没启动会话」这类前置状态如果按 HTTP 错误返回，用户只会看到
// 上一次的内容、得不到任何解释。与其在前端用 JS 强制交换错误响应，
// 不如让端点直接把状态当作内容渲染出来——这也是 htmx 的用法本意：
// 由服务端决定用户看到什么。
func (r *Renderer) RenderNote(message string) (string, error) {
	return r.execute("note.html", NoteData{Message: message})
}

// cloneUsage 复制用量指针，避免多个回合共享同一个 Entry 上的值。
func cloneUsage(u *sessions.Usage) *sessions.Usage {
	if u == nil {
		return nil
	}
	copy := *u
	return &copy
}
