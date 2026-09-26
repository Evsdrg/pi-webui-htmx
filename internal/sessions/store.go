// Package sessions 只读磁盘上的 Pi v3 会话文件，不启动 Pi，也不改写文件。
package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"pi-bridge-go/internal/jsonl"
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
	ID     string          `json:"id"`
	Kind   EntryKind       `json:"kind"`
	Text   string          `json:"text"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// ProjectEntries 把原始条目投影成渲染友好的结构。
// 解析失败的单条记录被跳过，不让一个坏条目毁掉整页。
func ProjectEntries(raw []json.RawMessage) []Entry {
	out := make([]Entry, 0, len(raw))
	for _, r := range raw {
		var item struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Summary string          `json:"summary"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(r, &item) != nil || item.ID == "" {
			continue
		}
		e := Entry{ID: item.ID, Detail: r}
		switch item.Type {
		case "message":
			role, text := messageRoleAndText(item.Message)
			switch role {
			case "user":
				e.Kind, e.Text = KindUser, text
			case "assistant":
				e.Kind, e.Text = KindAssistant, text
			case "toolResult":
				e.Kind, e.Text = KindTool, text
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

// Page 是一页按祖先到后代排序的历史记录。
type Page struct {
	SessionID     string            `json:"sessionId"`
	LeafID        string            `json:"leafId"`
	LeafSource    string            `json:"leafSource"`
	Entries       []json.RawMessage `json:"entries"`
	OldestEntryID string            `json:"oldestEntryId"`
	HasMore       bool              `json:"hasMore"`
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
	parent string
	offset int64
	size   int
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
	r := bufio.NewReader(io.LimitReader(f, st.Size()))
	nodes := map[string]node{}
	offset := int64(0)
	last := ""
	headerSeen := false
	for {
		if err = ctx.Err(); err != nil {
			return Page{}, err
		}
		b, n, e := jsonl.Read(r, s.limits.LineBytes)
		if errors.Is(e, io.EOF) || errors.Is(e, jsonl.ErrIncomplete) {
			break
		}
		if e != nil {
			return Page{}, protocol.E("limit_exceeded", "历史记录超过体积上限")
		}
		if !headerSeen {
			var current Header
			if json.Unmarshal(b, &current) != nil || current.Type != "session" || current.ID != id || current.Version != 3 || current.Cwd != h.Cwd {
				return Page{}, protocol.E("conflict", "会话头部已变化")
			}
			headerSeen = true
			offset += int64(n)
			continue
		}
		var item struct {
			Type   string          `json:"type"`
			ID     string          `json:"id"`
			Parent json.RawMessage `json:"parentId"`
		}
		if json.Unmarshal(b, &item) != nil || item.Type == "" || item.Type == "session" || !ValidID(item.ID) || len(item.Parent) == 0 {
			return Page{}, protocol.E("invalid_history", "完整的历史记录格式错误")
		}
		if _, ok := nodes[item.ID]; ok {
			return Page{}, protocol.E("invalid_history", "历史条目 ID 重复")
		}
		parent := ""
		if !bytes.Equal(item.Parent, []byte("null")) {
			if json.Unmarshal(item.Parent, &parent) != nil || !ValidID(parent) {
				return Page{}, protocol.E("invalid_history", "父条目 ID 无效")
			}
		}
		if parent != "" {
			if _, ok := nodes[parent]; !ok || parent == item.ID {
				return Page{}, protocol.E("invalid_history", "父链断裂或存在环")
			}
		}
		if len(nodes) >= s.limits.Entries {
			return Page{}, protocol.E("limit_exceeded", "历史索引条目数超过上限")
		}
		nodes[item.ID] = node{parent, offset, n}
		last = item.ID
		offset += int64(n)
	}
	if !headerSeen {
		return Page{}, protocol.E("invalid_history", "缺少完整的会话头部")
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
	selected := []string{}
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
	selected, total, cursor = s.alignToTurn(f, nodes, selected, total, cursor)
	page := Page{SessionID: id, LeafID: leaf, LeafSource: "disk", Entries: []json.RawMessage{}, HasMore: cursor != ""}
	for i := len(selected) - 1; i >= 0; i-- {
		node := nodes[selected[i]]
		b := make([]byte, node.size)
		if _, err = f.ReadAt(b, node.offset); err != nil {
			return Page{}, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		b = bytes.TrimSpace(b)
		if !json.Valid(b) {
			return Page{}, protocol.E("conflict", "读取期间历史文件发生变化")
		}
		page.Entries = append(page.Entries, json.RawMessage(b))
	}
	if len(selected) > 0 {
		page.OldestEntryID = selected[len(selected)-1]
	}
	return page, nil
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
// 边界情况：
//   - 一直取到会话开头都没遇到 user 消息：保留原切片，不强行扩大
//   - 体积超限：停在超限前，宁可留孤儿也不返回错误
//   - 读取失败：保留原切片
//
// 这一层是切片策略，不是正确性要求——前端本就能渲染孤儿条目。
func (s *Store) alignToTurn(f *os.File, nodes map[string]node, selected []string, total int, cursor string) ([]string, int, string) {
	if len(selected) == 0 {
		return selected, total, cursor
	}
	// 先看本页最旧一条是否已经是 user——是则无需对齐。
	// 注意 selected 是从新到旧排列的，末位是最旧。
	if kind := entryKind(s.readEntry(f, nodes, selected[len(selected)-1])); kind == KindUser {
		return selected, total, cursor
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
		b := s.readEntry(f, nodes, cursor)
		if !json.Valid(b) {
			break
		}
		selected = append(selected, cursor)
		total += node.size
		isUser := entryKind(b) == KindUser
		cursor = node.parent
		// 遇到 user 锚点说明已覆盖完整一轮，停止。
		if isUser {
			break
		}
	}
	return selected, total, cursor
}

// readEntry 读取某条原始记录；失败返回 nil。
func (s *Store) readEntry(f *os.File, nodes map[string]node, id string) []byte {
	node, ok := nodes[id]
	if !ok {
		return nil
	}
	b := make([]byte, node.size)
	if _, err := f.ReadAt(b, node.offset); err != nil {
		return nil
	}
	return bytes.TrimSpace(b)
}

// entryKind 判断一条原始记录的角色类别，用于轮边界对齐。
func entryKind(b []byte) EntryKind {
	entries := ProjectEntries([]json.RawMessage{json.RawMessage(b)})
	if len(entries) == 0 {
		return KindOther
	}
	return entries[0].Kind
}
