package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"

	"pi-bridge-go/internal/protocol"
)

// Tree 从磁盘记录投影出一棵会话树，形状与 Pi 的 get_tree 对齐。
//
// 用途：没有 worker 时浏览历史分支。浏览不该拉起 Pi 进程（U04）——
// 老实现直接要求 manager.Get，于是打开分支面板等于启动一个工作进程。
//
// 与 Pi 实时树的差异（不假装等价）：
//   - 不含内存态：未落盘的活跃分支这里看不到；
//   - fork 列表为空：fork 是写操作，仍必须显式启动会话。
func (s *Store) Tree(ctx context.Context, id string) (map[string]any, error) {
	h, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(h.path)
	if err != nil {
		return nil, protocol.E("not_found", "会话文件不存在")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, protocol.E("pi_error", "无法读取会话文件")
	}
	if !st.Mode().IsRegular() || st.Size() > s.limits.FileBytes {
		return nil, fileTooLargeError("历史文件", st.Size(), s.limits.FileBytes)
	}
	nodes, last, err := s.scanNodes(ctx, h, f, st.Size(), st.ModTime().UnixNano())
	if err != nil {
		return nil, err
	}

	// 文件顺序（偏移）等价于时间序；子节点列表按它排列。
	ids := make([]string, 0, len(nodes))
	for entryID := range nodes {
		ids = append(ids, entryID)
	}
	sort.Slice(ids, func(i, j int) bool { return nodes[ids[i]].offset < nodes[ids[j]].offset })

	type treeNode struct {
		id       string
		children []*treeNode
	}
	items := make(map[string]*treeNode, len(nodes))
	for _, entryID := range ids {
		items[entryID] = &treeNode{id: entryID}
	}
	var roots []*treeNode
	for _, entryID := range ids {
		n := nodes[entryID]
		if n.parent == "" {
			roots = append(roots, items[entryID])
			continue
		}
		parent := items[n.parent]
		if parent == nil {
			continue
		}
		parent.children = append(parent.children, items[entryID])
	}

	// 迭代摊平：长会话可能是上万层的线性链，递归会栈溢出（U08）。
	all := make([]*treeNode, 0, len(items))
	stack := make([]*treeNode, 0, len(roots))
	stack = append(stack, roots...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		all = append(all, n)
		stack = append(stack, n.children...)
	}
	wrapped := make(map[*treeNode]map[string]any, len(all))
	for _, n := range all {
		entry, err := s.treeEntry(f, nodes[n.id])
		if err != nil {
			return nil, err
		}
		wrapped[n] = map[string]any{"entry": entry}
	}
	for _, n := range all {
		if len(n.children) == 0 {
			continue
		}
		children := make([]any, 0, len(n.children))
		for _, c := range n.children {
			children = append(children, wrapped[c])
		}
		wrapped[n]["children"] = children
	}
	out := make([]any, 0, len(roots))
	for _, r := range roots {
		out = append(out, wrapped[r])
	}
	return map[string]any{"tree": out, "leafId": last}, nil
}

// treeEntry 读一条记录并投影成 Pi tree 里 entry 的最小形状。
// 摘要文本由内容里第一段纯文本提供；没有文本时退回角色说明。
func (s *Store) treeEntry(f *os.File, n node) (map[string]any, error) {
	buf := make([]byte, n.size)
	if _, err := f.ReadAt(buf, n.offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, protocol.E("pi_error", "读取历史记录失败")
	}
	var row struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(buf, &row) != nil || row.ID == "" {
		return nil, protocol.E("invalid_history", "历史记录无法解析")
	}
	out := map[string]any{"id": row.ID, "type": row.Type}
	if len(row.Message) > 0 {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(row.Message, &msg) == nil {
			inner := map[string]any{}
			if msg.Role != "" {
				inner["role"] = msg.Role
			}
			if text, ok := contentText(msg.Content); ok {
				out["text"] = shortTitle(text, 80)
			}
			out["message"] = inner
		}
	}
	return out, nil
}
