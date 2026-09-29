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
func DefaultLimits() Limits { return Limits{64 << 20, 8 << 20, 2 << 20, 100000, 10000} }

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
type Listing struct {
	Items     []Header `json:"items"`
	HasMore   bool     `json:"hasMore"`
	Truncated bool     `json:"truncated"`
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
func (s *Store) List(ctx context.Context, offset, limit int) (Listing, error) {
	if offset < 0 || limit < 1 || limit > 200 {
		return Listing{}, protocol.E("invalid_params", "分页参数无效")
	}
	items, hasMore, truncated, err := s.index.Page(ctx, offset, limit)
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
	return Listing{Items: out, HasMore: hasMore, Truncated: truncated}, nil
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
		return Page{}, protocol.E("limit_exceeded", "历史文件超过体积上限")
	}
	// 先查缓存：翻页与切标签时文件不变，可省掉整个解析阶段。
	// 失效判定见 scanCache 的说明——size、mtime 与文件身份都要一致。
	// 文件身份必须用绝对路径取：索引里存的是相对路径。
	abs := filepath.Join(s.dir, filepath.FromSlash(h.path))
	nodes, last, cached := s.scan.get(abs, st.Size(), st.ModTime().UnixNano())
	if !cached {
		var scanErr error
		nodes, last, scanErr = s.scanFile(ctx, f, st.Size(), id, h.Cwd)
		if scanErr != nil {
			return Page{}, scanErr
		}
		s.scan.put(abs, st.Size(), st.ModTime().UnixNano(), nodes, last)
	}
	if leaf == "" {
		leaf = last
	}
	if leaf != "" {
		if _, ok := nodes[leaf]; !ok {
			return Page{}, protocol.E("not_found", "叶子条目不存在")
		}
	}
	start := leaf
	if before != "" {
		for start != "" && start != before {
			start = nodes[start].parent
		}
		if start == "" {
			return Page{}, protocol.E("conflict", "分页游标不在所选分支上")
		}
		start = nodes[start].parent
	}
	// 预分配：条目数已知上界（limit），避免 append 逐次增长拷贝。
	// alignToTurn 可能再多取一些，那时 append 会自行扩容。
	selected := make([]string, 0, limit)
	total := 0
	cursor := start
	for cursor != "" && len(selected) < limit {
		node := nodes[cursor]
		if total+node.size > s.limits.PageBytes {
			if len(selected) == 0 {
				return Page{}, protocol.E("limit_exceeded", "单条历史记录超过单页体积上限")
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
		if _, err = f.ReadAt(slot, node.offset); err != nil {
			return Page{}, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		trimmed := bytes.TrimSpace(slot)
		if !json.Valid(trimmed) {
			return Page{}, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		page.Entries = append(page.Entries, json.RawMessage(trimmed))
		at += node.size
	}
	if len(selected) > 0 {
		page.OldestEntryID = selected[len(selected)-1]
	}
	page.HistoricalModel, err = historicalModel(ctx, f, nodes, leaf, before, page.Entries)
	if err != nil {
		return Page{}, err
	}
	return page, nil
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
