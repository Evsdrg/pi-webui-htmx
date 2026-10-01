package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"pi-bridge-go/internal/protocol"
)

// ExportRow 是导出文档里的一行。行是平铺的：导出不需要树结构，
// 也就不需要在渲染时递归——深链会话因此不会栈溢出（B45）。
type ExportRow struct {
	// Kind 是行类型：user / assistant / thinking / toolCall / toolResult / info。
	Kind string `json:"kind"`
	// Label 用于工具名等短标注，可为空。
	Label string `json:"label,omitempty"`
	Text  string `json:"text"`
}

// ExportDocument 是只读导出所需的全部内容。
type ExportDocument struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Cwd       string      `json:"cwd"`
	Leaf      string      `json:"leaf"`
	Truncated bool        `json:"truncated"`
	Rows      []ExportRow `json:"rows"`
}

// ExportLimits 约束导出文档的规模：导出是给人看的归档，
// 不是第二份会话文件，超长内容按行截断并标记。
type ExportLimits struct {
	MaxRows      int
	MaxTextBytes int
}

// DefaultExportLimits 给出默认导出限额。
func DefaultExportLimits() ExportLimits {
	return ExportLimits{MaxRows: 20000, MaxTextBytes: 8 << 10}
}

// ExportDocument 按文件顺序投影出只读导出文档。
// 不启动 Pi：导出磁盘历史是一条纯读取路径（B76）。
func (s *Store) ExportDocument(ctx context.Context, id string, limits ExportLimits) (ExportDocument, error) {
	h, err := s.Find(ctx, id)
	if err != nil {
		return ExportDocument{}, err
	}
	f, err := s.root.Open(h.path)
	if err != nil {
		return ExportDocument{}, protocol.E("not_found", "会话文件不存在")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ExportDocument{}, protocol.E("pi_error", "无法读取会话文件")
	}
	if !st.Mode().IsRegular() || st.Size() > s.limits.FileBytes {
		return ExportDocument{}, fileTooLargeError("历史文件", st.Size(), s.limits.FileBytes)
	}
	// 导出要遍历整条会话，尾部窗口不够——直接全扫。
	nodes, last, err := s.scanFullAll(ctx, h, f, st.Size(), st.ModTime().UnixNano())
	if err != nil {
		return ExportDocument{}, err
	}
	// 文件顺序（偏移）就是时间序。
	ids := make([]string, 0, len(nodes))
	for entryID := range nodes {
		ids = append(ids, entryID)
	}
	sort.Slice(ids, func(i, j int) bool { return nodes[ids[i]].offset < nodes[ids[j]].offset })

	doc := ExportDocument{ID: h.ID, Name: h.Name, Cwd: h.Cwd, Leaf: last, Rows: make([]ExportRow, 0, len(ids))}
	for _, entryID := range ids {
		if err := ctx.Err(); err != nil {
			return ExportDocument{}, err
		}
		if len(doc.Rows) >= limits.MaxRows {
			doc.Truncated = true
			break
		}
		n := nodes[entryID]
		buf := make([]byte, n.size)
		if _, err := f.ReadAt(buf, n.offset); err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		rows := exportRows(buf, limits.MaxTextBytes)
		if len(doc.Rows)+len(rows) > limits.MaxRows {
			doc.Rows = append(doc.Rows, rows[:limits.MaxRows-len(doc.Rows)]...)
			doc.Truncated = true
			break
		}
		doc.Rows = append(doc.Rows, rows...)
	}
	return doc, nil
}

// exportRows 把一条记录投影成若干导出行。
// 解析失败的记录直接跳过：导出是归档视图，不该被一条坏记录整体打断。
func exportRows(line []byte, maxText int) []ExportRow {
	var row struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &row) != nil || row.ID == "" {
		return nil
	}
	switch row.Type {
	case "session_info":
		if row.Name == "" {
			return nil
		}
		return []ExportRow{{Kind: "info", Label: "标题", Text: clipText(row.Name, maxText)}}
	case "model_change":
		return []ExportRow{{Kind: "info", Label: "模型", Text: clipText(row.ID, maxText)}}
	case "message":
	default:
		return nil
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Command string          `json:"command"`
		Output  string          `json:"output"`
		Tool    string          `json:"toolName"`
	}
	if json.Unmarshal(row.Message, &msg) != nil {
		return nil
	}
	switch msg.Role {
	case "user":
		text := flattenContent(msg.Content)
		if text == "" {
			return nil
		}
		return []ExportRow{{Kind: "user", Text: clipText(text, maxText)}}
	case "assistant":
		return assistantRows(msg.Content, maxText)
	case "toolResult":
		text := flattenContent(msg.Content)
		if text == "" {
			// 工具结果常见形状是字符串化 JSON 或 {command,output}。
			text = flattenToolResult(msg.Content)
		}
		if text == "" {
			text = msg.Command
		}
		if text == "" {
			text = msg.Output
		}
		if text == "" {
			return nil
		}
		return []ExportRow{{Kind: "toolResult", Label: msg.Tool, Text: clipText(text, maxText)}}
	}
	text := flattenContent(msg.Content)
	if text == "" {
		return nil
	}
	return []ExportRow{{Kind: msg.Role, Text: clipText(text, maxText)}}
}

// assistantRows 展开 assistant 的内容块：文本合并成一行，
// 思考与工具调用各占一行，顺序与模型输出的顺序一致。
func assistantRows(content json.RawMessage, maxText int) []ExportRow {
	if shapeOf(content) == shapeString {
		var s string
		if json.Unmarshal(content, &s) == nil && s != "" {
			return []ExportRow{{Kind: "assistant", Text: clipText(s, maxText)}}
		}
		return nil
	}
	if shapeOf(content) != shapeArray {
		return nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	out := make([]ExportRow, 0, len(blocks))
	var text strings.Builder
	flushText := func() {
		if text.Len() > 0 {
			out = append(out, ExportRow{Kind: "assistant", Text: clipText(text.String(), maxText)})
			text.Reset()
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(b.Text)
		case "thinking":
			flushText()
			if b.Thinking != "" {
				out = append(out, ExportRow{Kind: "thinking", Text: clipText(b.Thinking, maxText)})
			}
		case "toolCall":
			flushText()
			out = append(out, ExportRow{Kind: "toolCall", Label: b.Name, Text: clipText(string(b.Arguments), maxText)})
		}
	}
	flushText()
	return out
}

// flattenToolResult 处理工具结果的非文本形状：常见是 {command,output} 或纯数组。
func flattenToolResult(content json.RawMessage) string {
	if shapeOf(content) != shapeObject {
		return ""
	}
	var v struct {
		Command string `json:"command"`
		Output  string `json:"output"`
	}
	if json.Unmarshal(content, &v) != nil {
		return ""
	}
	if v.Output != "" {
		return v.Output
	}
	return v.Command
}

// clipText 按 UTF-8 边界截断文本并标记，避免导出文档被单条超长内容撑爆。
func clipText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…（已截断）"
}
