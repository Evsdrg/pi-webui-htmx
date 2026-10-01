// Package sessions 只读磁盘上的 Pi v3 会话文件，不启动 Pi，也不改写文件。
package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/workspace"
)

// Limits 是历史读取的体积上限，避免超大会话拖垮桥。
type Limits struct {
	FileBytes                            int64
	LineBytes, PageBytes, Entries, Files int
}

// DefaultLimits 给出默认体积上限：文件、单行、单页、条目数与会话文件数。
//
// FileBytes 为什么是 256 MiB：它决定「多大的会话还能在网页里打开」，所以
// 取大小取决于真实会话的分布与全扫的开销。实测（真实记录构成的会话，线性
// 可外推）：
//
//	 90 MB / 24489 条   冷扫描 125 ms，分配 8.5 MiB，堆 +4.6 MiB
//	262 MB / 73119 条   冷扫描 393 ms，分配 26.6 MiB，堆 +17.3 MiB
//
// 扫描只在首次打开时发生：命中扫描缓存后首页是 200 µs（见 cache.go）。
// 本机实际最大的会话是 86.8 MB（跑了几周的 CLI 会话）——原先的 64 MiB
// 上限把它挡在外面，用户点开只看到一句「超过体积上限」，完全无法浏览；
// 而扫描它只花 125 ms，属于得不偿失的过度保守。
func DefaultLimits() Limits { return Limits{256 << 20, 8 << 20, 2 << 20, 100000, 10000} }

// Store 通过 os.Root 访问受管会话目录，防止路径穿越与符号链接逃逸。
// 列表与查找走内存索引，历史读取按需解析单个文件。
type Store struct {
	root   *os.Root
	dir    string
	policy *workspace.Policy
	limits Limits
	index  *Index

	// scan 缓存最近一次全文件扫描的结果，见 scanCache 的说明。
	// 翻页时文件不变，可把约八成的解析开销省掉。
	scan scanCache
}

// fileTooLargeError 报出实际大小与上限。
//
// 只说「超过体积上限」用户无法行动：不知道自己离上限有多远、也无从判断
// 是不是换个会话就能看。带上两个数字后，至少能看出是「略微超一点」还是
// 「大了几十倍」。错误码保持 limit_exceeded 不变，只有文案变具体。
func fileTooLargeError(kind string, size, limit int64) error {
	return protocol.E("limit_exceeded", fmt.Sprintf("%s %s 超过 %s 上限", kind, humanBytes(size), humanBytes(limit)))
}

// humanBytes 把字节数写成便于阅读的形式，保留一位小数。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, suffix)
		}
	}
	return fmt.Sprintf("%.1f TiB", v/unit)
}

// Header 是会话首条记录的解析结果，path 不对外暴露。
type Header struct {
	Type      string    `json:"type"`
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Cwd       string    `json:"cwd"`
	Name      string    `json:"name,omitempty"`
	Timestamp string    `json:"timestamp"`
	Modified  time.Time `json:"modified"`
	path      string
}

// EntryKind 是投影后的条目类别。
type EntryKind string

const (
	KindUser       EntryKind = "user"
	KindAssistant  EntryKind = "assistant"
	KindTool       EntryKind = "tool"
	KindCompaction EntryKind = "compaction"
	KindOther      EntryKind = "other"
)

// Entry 是一条历史条目的投影，只含渲染所需字段。
// 原始 JSONL 记录保留在 Page.Entries 中，需要完整结构时用它。
type Entry struct {
	ID    string    `json:"id"`
	Kind  EntryKind `json:"kind"`
	Text  string    `json:"text"`
	Error string    `json:"error,omitempty"`
	// Usage 只对 assistant 条目有意义：Pi 把 token 与费用写在
	// message.usage 上，与 stopReason 同级。它不参与任何派生计算，
	// 原样带到投影层，由展示层决定怎么汇总与格式化。
	Usage  *Usage          `json:"usage,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
	// Lazy 列出可延后加载的内容块（思考、工具图片）。
	// 只带索引不带内容：历史页因此能渲染占位符，而不把大块数据传出去。
	Lazy []LazyBlock `json:"lazy,omitempty"`
	// Timestamp 是条目写入时间。时长统计完全由它推导：Pi 不记录单块
	// 耗时，JSONL 里没有 durationMs 这类字段。零值表示时间戳缺失或
	// 解析失败，调用方据此跳过时长而不是当成 1970 年。
	Timestamp time.Time `json:"timestamp"`
	// ToolName 是工具结果对应的工具名；非工具结果为空。
	ToolName string `json:"toolName,omitempty"`
	// Failed 表示工具结果报错，与 Pi Web 的 isError 同义。
	// 工具块的边框与配色由它决定。
	Failed bool `json:"failed,omitempty"`
}

// ProjectEntries 把原始条目投影成渲染友好的结构。
// 解析失败的单条记录被跳过，不让一个坏条目毁掉整页。
func ProjectEntries(raw []json.RawMessage) []Entry {
	out := make([]Entry, 0, len(raw))
	for _, r := range raw {
		var item struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Summary   string          `json:"summary"`
			Message   json.RawMessage `json:"message"`
			Timestamp string          `json:"timestamp"`
		}
		if json.Unmarshal(r, &item) != nil || item.ID == "" {
			continue
		}
		e := Entry{ID: item.ID, Detail: r, Lazy: scanLazyBlocks(item.Message), Timestamp: parseTimestamp(item.Timestamp)}
		switch item.Type {
		case "message":
			role, text := messageRoleAndText(item.Message)
			switch role {
			case "user":
				e.Kind, e.Text = KindUser, text
			case "assistant":
				e.Kind, e.Text = KindAssistant, text
				e.Error = assistantError(item.Message)
				e.Usage = parseUsage(item.Message)
			case "toolResult":
				e.Kind, e.Text = KindTool, text
				e.ToolName, e.Failed = toolResultMeta(item.Message)
			default:
				e.Kind, e.Text = KindOther, text
			}
		case "compaction":
			e.Kind, e.Text = KindCompaction, item.Summary
		default:
			e.Kind, e.Text = KindOther, item.Summary
		}
		out = append(out, e)
	}
	return out
}

// messageRoleAndText 取出消息的角色与纯文本。
func messageRoleAndText(raw json.RawMessage) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Command string          `json:"command"`
		Output  string          `json:"output"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return "", ""
	}
	role := msg.Role
	if role == "toolResult" || (role == "" && msg.Command != "") {
		role = "toolResult"
	}
	text := flattenContent(msg.Content)
	if text == "" {
		text = msg.Command
	}
	if text == "" {
		text = msg.Output
	}
	return role, text
}

// ModelRef 是会话文件记录的历史模型标识，不代表当前仍可用。
type ModelRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// Page 是一页按祖先到后代排序的历史记录。
type Page struct {
	SessionID       string            `json:"sessionId"`
	LeafID          string            `json:"leafId"`
	LeafSource      string            `json:"leafSource"`
	Entries         []json.RawMessage `json:"entries"`
	OldestEntryID   string            `json:"oldestEntryId"`
	HasMore         bool              `json:"hasMore"`
	HistoricalModel *ModelRef         `json:"historicalModel,omitempty"`
}

// Listing 是会话目录分页结果。
// Listing 是一页会话列表。
//
// Cwd 与 Cwds 服务于「按工作区筛选」：前者回显当前筛选（空串表示未筛选），
// 后者给出已知工作区及各自的会话数，用来渲染下拉项。两者都由这里返回，
// 而不是让前端从已渲染的行里自己收集——那样只能看到当前页的工作区，
// 第二页、筛选后的列表都会给出不完整的候选。
type Listing struct {
	Items     []Header   `json:"items"`
	HasMore   bool       `json:"hasMore"`
	Truncated bool       `json:"truncated"`
	Cwd       string     `json:"cwd,omitempty"`
	Cwds      []CwdCount `json:"cwds,omitempty"`
	// Groups 只在分组视图（view=workspace）下填充：按工作区分组，
	// 每组只带最近若干条。分组视图不做分页——它要回答的是
	// 「我有哪些工作区、各自最近在忙什么」，翻页由「查看该工作区全部」
	// 切回时间线视图完成。
	Groups []Group `json:"groups,omitempty"`
}

// Group 是一个工作区及其最近若干条会话。
type Group struct {
	Cwd   string   `json:"cwd"`
	Total int      `json:"total"`
	Items []Header `json:"items"`
}

// CwdCount 是一个工作区及其会话数。
type CwdCount struct {
	Cwd   string `json:"cwd"`
	Count int    `json:"count"`
	// Filtered 标记这是当前选中的工作区，模板据此决定 selected。
	Filtered bool `json:"filtered,omitempty"`
}

func New(dir string, p *workspace.Policy, limits Limits) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	return &Store{
		root:   r,
		dir:    abs,
		policy: p,
		limits: limits,
		index:  NewIndex(r, abs, p, limits, 2*time.Second),
	}, nil
}

// Index 返回内部索引，供诊断端点使用。
func (s *Store) Index() *Index { return s.index }
func (s *Store) Close() error  { return s.root.Close() }

// Dir 返回受管会话目录的绝对路径，供启动 Pi 时指定 --session-dir。
func (s *Store) Dir() string { return s.dir }

// ValidID 限制会话与条目 ID 只包含安全字符，避免外部输入构造路径。
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// List 按最近修改时间倒序返回会话目录。
// 命中索引缓存时不做任何磁盘遍历。
// List 返回一页会话；cwd 非空时只返回该工作区的会话。
//
// 筛选发生在分页之前，所以 offset/HasMore 都是针对筛选后的集合——
// 否则「加载更多」会在筛选后重复或跳过条目。
// GroupedPerCwd 是分组视图里每组默认展示的条数。
//
// 取 5 是因为侧栏一屏最多显示十来个会话，每组 5 条已经能看出
// 「这个项目最近在做什么」，而组数不受限制时也不会把列表撑得太长。
const GroupedPerCwd = 5

// ListGrouped 按工作区分组返回会话，每组最近 GroupedPerCwd 条。
func (s *Store) ListGrouped(ctx context.Context, perGroup int) (Listing, error) {
	groups, truncated, err := s.index.Groups(ctx, perGroup)
	if err != nil {
		return Listing{}, err
	}
	out := make([]Group, 0, len(groups))
	cwds := make([]CwdCount, 0, len(groups))
	for _, g := range groups {
		items := make([]Header, 0, len(g.Items))
		for _, e := range g.Items {
			items = append(items, Header{
				Type: "session", Version: e.version, ID: e.id, Cwd: e.cwd, Name: e.name,
				Timestamp: e.timestamp, Modified: e.modified, path: e.path,
			})
		}
		out = append(out, Group{Cwd: g.Cwd, Total: g.Total, Items: items})
		cwds = append(cwds, CwdCount{Cwd: g.Cwd, Count: g.Total})
	}
	return Listing{Truncated: truncated, Cwds: cwds, Groups: out}, nil
}

func (s *Store) List(ctx context.Context, offset, limit int, cwd string) (Listing, error) {
	if offset < 0 || limit < 1 || limit > 200 {
		return Listing{}, protocol.E("invalid_params", "分页参数无效")
	}
	cwd = normalizeCwd(cwd)
	items, hasMore, truncated, err := s.index.Page(ctx, offset, limit, cwd)
	if err != nil {
		return Listing{}, err
	}
	out := make([]Header, 0, len(items))
	for _, e := range items {
		out = append(out, Header{
			Type: "session", Version: e.version, ID: e.id, Cwd: e.cwd, Name: e.name,
			Timestamp: e.timestamp, Modified: e.modified, path: e.path,
		})
	}
	// 工作区清单在筛选之后统计：用户看到的计数应与「点下去会得到多少条」一致。
	cwds, err := s.index.Workspaces(ctx)
	if err != nil {
		return Listing{}, err
	}
	return Listing{Items: out, HasMore: hasMore, Truncated: truncated, Cwd: cwd, Cwds: cwds}, nil
}

// normalizeCwd 把工作区路径归一化成可比较的形式。
// 会话里存的是 Pi 写入的绝对路径，一般不带尾斜杠，但手改过的配置可能带。
func normalizeCwd(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || cwd == "/" {
		return cwd
	}
	return strings.TrimRight(cwd, "/")
}

// Find 按会话 ID 查找会话元数据。
func (s *Store) Find(ctx context.Context, id string) (Header, error) {
	if !ValidID(id) {
		return Header{}, protocol.E("invalid_params", "会话 ID 无效")
	}
	e, err := s.index.Lookup(ctx, id)
	if err != nil {
		return Header{}, err
	}
	return Header{Type: "session", Version: e.version, ID: e.id, Cwd: e.cwd, Name: e.name, Timestamp: e.timestamp, Modified: e.modified, path: e.path}, nil
}

// Path 返回会话文件的绝对路径，仅用于启动受管 Pi 进程。
func (s *Store) Path(h Header) string { return filepath.Join(s.dir, filepath.FromSlash(h.path)) }

// node 记录一条历史记录在文件中的位置与父节点，用于按分支反向取页。
type node struct {
	parent      string
	offset      int64
	size        int
	lastModelID string
	// isUser 在这条记录是不是 user 消息（即轮边界对齐要找的锚点）。
	// 它在扫描时顺手记下（见 parseEntryHead）：alignToTurn 以前为了
	// 这一个布尔值把整条记录读回来做一次完整投影，在首页基准里
	// 占约 31KB/op。
	isUser bool
}

// scanNodes 返回会话文件的父链索引：命中扫描缓存就直接用，
// 否则全扫一次并写入缓存。History、惰性读取与磁盘树共用它（O03/O04）。
// scanNodes 返回可用于取页的索引。
//
// 先查缓存；未命中只扫尾部窗口（快），并告知窗口是否覆盖整个文件。
// 窗口不够时由调用方走 scanFullAll。
func (s *Store) scanNodes(ctx context.Context, h Header, f *os.File, size, mtime int64, minNodes int) (map[string]node, string, bool, error) {
	// 缓存键用绝对路径：索引里存的是相对路径。
	abs := filepath.Join(s.dir, filepath.FromSlash(h.path))
	if nodes, last, complete, ok := s.scan.get(abs, size, mtime); ok {
		return nodes, last, complete, nil
	}
	nodes, last, complete, err := s.scanTail(ctx, f, size, h.ID, h.Cwd, minNodes)
	if err != nil {
		return nil, "", false, err
	}
	s.scan.put(abs, size, mtime, nodes, last, complete)
	return nodes, last, complete, nil
}

// scanFullAll 全量扫描并缓存（记为 complete）。
// 只在尾部窗口不够用时才走到这里。
func (s *Store) scanFullAll(ctx context.Context, h Header, f *os.File, size, mtime int64) (map[string]node, string, error) {
	abs := filepath.Join(s.dir, filepath.FromSlash(h.path))
	nodes, last, err := s.scanFile(ctx, f, size, h.ID, h.Cwd)
	if err != nil {
		return nil, "", err
	}
	s.scan.put(abs, size, mtime, nodes, last, true)
	return nodes, last, nil
}

// History 读取所选分支上的一页历史。
// leaf 缺省取磁盘上可恢复的叶子；before 必须是该分支的祖先条目。
// 忽略末尾没有 LF 的半行，但完整的损坏行一律显式报错。
func (s *Store) History(ctx context.Context, id, leaf, before string, limit int) (Page, error) {
	if limit < 1 || limit > 200 {
		return Page{}, protocol.E("invalid_params", "limit 必须在 1 到 200 之间")
	}
	h, err := s.Find(ctx, id)
	if err != nil {
		return Page{}, err
	}
	f, err := s.root.Open(h.path)
	if err != nil {
		return Page{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Page{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > s.limits.FileBytes {
		return Page{}, fileTooLargeError("历史文件", st.Size(), s.limits.FileBytes)
	}
	// 先把尾部窗口建出来（未命中缓存时只读最后几 MiB），首页与相邻翻页
	// 都落在窗口内；窗口不够用（要看的页比窗口更早）时才退到全扫。
	//
	// +64 是给轮边界对齐留的余量：alignToTurn 会向前多取到 user 锚点。
	// 多要几十条比全扫便宜几个数量级。
	mtime := st.ModTime().UnixNano()
	want := limit + 64
	nodes, last, complete, err := s.scanNodes(ctx, h, f, st.Size(), mtime, want)
	if err != nil {
		return Page{}, err
	}
	page, ok, err := s.historyPage(ctx, f, id, leaf, before, limit, nodes, last, complete)
	if err != nil {
		return Page{}, err
	}
	if ok {
		return page, nil
	}
	// 窗口不够：全扫一次，之后这份完整索引会进缓存。
	nodes, last, err = s.scanFullAll(ctx, h, f, st.Size(), mtime)
	if err != nil {
		return Page{}, err
	}
	page, _, err = s.historyPage(ctx, f, id, leaf, before, limit, nodes, last, true)
	return page, err
}

// historyPage 用给定索引取一页历史。
//
// 返回 ok=false 表示这份索引不足以回答这次请求（尾部窗口不够早），
// 调用方应用完整索引重试；complete 为真时不会出现 ok=false——
// 那时「找不到叶子」「游标不在分支上」都是真实的业务错误，直接返回。
func (s *Store) historyPage(ctx context.Context, f *os.File, id, leaf, before string, limit int, nodes map[string]node, last string, complete bool) (Page, bool, error) {
	if leaf == "" {
		leaf = last
	}
	if leaf != "" {
		if _, ok := nodes[leaf]; !ok {
			if complete {
				return Page{}, false, protocol.E("not_found", "叶子条目不存在")
			}
			return Page{}, false, nil
		}
	}
	start := leaf
	if before != "" {
		for start != "" && start != before {
			n, ok := nodes[start]
			if !ok {
				if complete {
					return Page{}, false, protocol.E("conflict", "分页游标不在所选分支上")
				}
				return Page{}, false, nil
			}
			start = n.parent
		}
		if start == "" {
			if complete {
				return Page{}, false, protocol.E("conflict", "分页游标不在所选分支上")
			}
			return Page{}, false, nil
		}
		start = nodes[start].parent
	}
	// 预分配：条目数已知上界（limit），避免 append 逐次增长拷贝。
	// alignToTurn 可能再多取一些，那时 append 会自行扩容。
	selected := make([]string, 0, limit)
	total := 0
	cursor := start
	for cursor != "" && len(selected) < limit {
		node, ok := nodes[cursor]
		if !ok {
			// 走到窗口边界：页还没取够，交给全扫重来。
			return Page{}, false, nil
		}
		if total+node.size > s.limits.PageBytes {
			if len(selected) == 0 {
				return Page{}, false, protocol.E("limit_exceeded", "单条历史记录超过单页体积上限")
			}
			break
		}
		total += node.size
		selected = append(selected, cursor)
		cursor = node.parent
	}

	// 轮边界对齐：见 alignToTurn 的说明。它会向前多取条目，
	// 因此返回更新后的游标，HasMore 必须用它而不是原 cursor。
	selected, cursor = s.alignToTurn(nodes, selected, total, cursor)
	// 对齐可能多走了几步，若已踩到窗口边界就退到全扫重来——
	// 否则会给出比预期短的一页，而且不报错。
	// 对齐可能多走了几步，若已踩到窗口边界就交给全扫——否则会给出
	// 比预期短的一页，而且不报错。HasMore 也可能因此算错。
	if !complete && cursor != "" {
		if _, ok := nodes[cursor]; !ok {
			return Page{}, false, nil
		}
	}

	// 整页只分配一次：条目长度之和就是需要的字节数，逐条 ReadAt 填进去。
	// 以前是每条一次 make([]byte, node.size)——分配次数与条目数同阶，
	// 而真正必须活下来的只是字节本身（Page.Entries 要留到渲染完）。
	// 各条目是这块缓冲的子切片，因此它的生命周期由 Entries 决定。
	//
	// 长度在这里重算而不是沿用 total：alignToTurn 会追加条目，
	// 它不再回传累加值（那个值除本处外无人使用）。
	total = 0
	for _, id := range selected {
		total += nodes[id].size
	}
	buf := make([]byte, total)
	page := Page{SessionID: id, LeafID: leaf, LeafSource: "disk", Entries: make([]json.RawMessage, 0, len(selected)), HasMore: cursor != ""}
	at := 0
	for i := len(selected) - 1; i >= 0; i-- {
		node := nodes[selected[i]]
		slot := buf[at : at+node.size]
		if _, err := f.ReadAt(slot, node.offset); err != nil {
			return Page{}, false, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		trimmed := bytes.TrimSpace(slot)
		if !json.Valid(trimmed) {
			return Page{}, false, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		page.Entries = append(page.Entries, json.RawMessage(trimmed))
		at += node.size
	}
	if len(selected) > 0 {
		page.OldestEntryID = selected[len(selected)-1]
	}
	model, err := historicalModel(ctx, f, nodes, leaf, before, page.Entries)
	if err != nil {
		return Page{}, false, err
	}
	page.HistoricalModel = model
	return page, true, nil
}

// historicalModel 优先使用当前叶子最近一次模型切换；首页若有更新的助手回复，
// 也可恢复没有显式 model_change 的旧会话。历史标识不代表当前模型仍可用。
func historicalModel(ctx context.Context, f *os.File, nodes map[string]node, leaf, before string, entries []json.RawMessage) (*ModelRef, error) {
	modelID := ""
	if leaf != "" {
		modelID = nodes[leaf].lastModelID
	}
	if before == "" {
		for i := len(entries) - 1; i >= 0; i-- {
			raw := entries[i]
			if !bytes.Contains(raw, []byte(`"provider"`)) || !bytes.Contains(raw, []byte(`"model"`)) {
				continue
			}
			var item struct {
				ID      string `json:"id"`
				Message struct {
					Role     string `json:"role"`
					Provider string `json:"provider"`
					Model    string `json:"model"`
				} `json:"message"`
			}
			if json.Unmarshal(raw, &item) == nil && item.Message.Role == "assistant" && item.Message.Provider != "" && item.Message.Model != "" && nodes[item.ID].lastModelID == modelID {
				return &ModelRef{Provider: item.Message.Provider, ID: item.Message.Model}, nil
			}
		}
	}
	if modelID == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := nodes[modelID]
	raw := make([]byte, n.size)
	if _, err := f.ReadAt(raw, n.offset); err != nil {
		return nil, protocol.E("conflict", "读取期间历史文件发生变化")
	}
	var change struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	}
	if json.Unmarshal(raw, &change) != nil {
		return nil, protocol.E("conflict", "读取期间历史文件发生变化")
	}
	if change.Provider != "" && change.ModelID != "" {
		return &ModelRef{Provider: change.Provider, ID: change.ModelID}, nil
	}
	return nil, nil
}

// alignToTurn 把分页边界对齐到「完整的一轮」。
//
// 为什么需要：直接按原始条目数切片会把一轮对话切成两半。翻页回来的
// 第一屏边界上会出现没有 user 锚点的孤儿工具/助手条目，而上一屏的末尾
// 正是这些条目——它们会被重复显示，视口也被顶走。
//
// 做法：从本页最旧一端（selected 的最后一个）向前多取，直到遇到一条
// user 消息，使本页以「某个完整轮的结尾」收尾。向前多取的部分不计入
// limit（那是 UI 层面的预算），但仍受单页体积与条目数硬上限约束。
//
// 判定只看扫描期就记好的 node.isUser，因此这里既不读盘也不反序列化。
// 代价是「读期间文件变化」不再在这一层被发现：被多取的条目仍会进入
// 页循环逐条 ReadAt + json.Valid，那里失败会报 conflict，而不是静默缩短。
//
// 边界情况：
//   - 一直取到会话开头都没遇到 user 消息：保留原切片，不强行扩大
//   - 体积或条目数超限：停在超限前，宁可留孤儿也不返回错误
//
// 这一层是切片策略，不是正确性要求——前端本就能渲染孤儿条目。
func (s *Store) alignToTurn(nodes map[string]node, selected []string, total int, cursor string) ([]string, string) {
	if len(selected) == 0 {
		return selected, cursor
	}
	// 先看本页最旧一条是否已经是 user——是则无需对齐。
	// 注意 selected 是从新到旧排列的，末位是最旧。
	// 判定用扫描期记下的 isUser：不再读盘、不再解析整条记录。
	if nodes[selected[len(selected)-1]].isUser {
		return selected, cursor
	}
	// 否则向前多取，直到把某个完整轮的 user 锚点包含进来。
	// 从 selected 末位的父亲继续向前——调用方传进来的 cursor 正是它。
	for cursor != "" {
		node := nodes[cursor]
		if total+node.size > s.limits.PageBytes {
			break
		}
		if len(selected) >= s.limits.Entries {
			break
		}
		selected = append(selected, cursor)
		total += node.size
		cursor = node.parent
		// 遇到 user 锚点说明已覆盖完整一轮，停止。
		if node.isUser {
			break
		}
	}
	return selected, cursor
}

// Usage 是一条 assistant 消息的 token 与费用。零值表示「没有记录」，
// 与「记录为零」是两件事：前者不渲染，后者按 Pi 的规则整段省略。
type Usage struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Cost       float64 `json:"cost"`
}

// parseUsage 从 assistant 消息里取出 usage。字段缺失或形状不符时返回 nil，
// 不返回全零值——调用方据此区分「没有记录」与「记录为零」。
func parseUsage(raw json.RawMessage) *Usage {
	if len(raw) == 0 {
		return nil
	}
	var msg struct {
		Usage *struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
			Cost       struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &msg) != nil || msg.Usage == nil {
		return nil
	}
	u := msg.Usage
	return &Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Cost: u.Cost.Total}
}

// Summary 按 Pi Web 的格式输出用量摘要：零值整段省略，千分位分隔。
// 这里的取舍与 Pi Web 的 formatUsage 一致——四个 token 字段与费用各自
// 独立决定是否出现，因此「只有缓存读取」的记录不会显示成空串。
func (u Usage) Summary() string {
	parts := make([]string, 0, 5)
	if u.Input != 0 {
		parts = append(parts, fmt.Sprintf("%d in", u.Input))
	}
	if u.Output != 0 {
		parts = append(parts, fmt.Sprintf("%d out", u.Output))
	}
	if u.CacheRead != 0 {
		parts = append(parts, fmt.Sprintf("%d cache R", u.CacheRead))
	}
	if u.CacheWrite != 0 {
		parts = append(parts, fmt.Sprintf("%d cache W", u.CacheWrite))
	}
	if u.Cost != 0 {
		parts = append(parts, "$"+strconv.FormatFloat(u.Cost, 'f', 4, 64))
	}
	return strings.Join(parts, " · ")
}

// parseTimestamp 解析条目时间戳。解析失败返回零值，调用方据此跳过
// 时长显示，而不是把零值当成 1970 年。
func parseTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return ts
}

// toolResultMeta 取出工具名与失败标记。
func toolResultMeta(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var msg struct {
		ToolName string `json:"toolName"`
		IsError  bool   `json:"isError"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return "", false
	}
	return msg.ToolName, msg.IsError
}

// ElapsedSeconds 计算整秒差，四舍五入到最接近的整秒。
// 任一时间戳为零值、或差值不足以进位的返回 0，调用方据此不显示时长——
// 与 Pi Web 的 `Math.round(ms/1000)` 加 `secs > 0 ? secs : undefined` 一致：
// 给一个 42 毫秒的步骤显示 "0s" 只会干扰阅读。
func ElapsedSeconds(from, to time.Time) int {
	if from.IsZero() || to.IsZero() {
		return 0
	}
	secs := int(math.Round(to.Sub(from).Seconds()))
	if secs <= 0 {
		return 0
	}
	return secs
}
