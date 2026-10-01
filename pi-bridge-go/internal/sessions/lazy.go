package sessions

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
)

// 惰性内容的上限。thinking 是纯文本，图片是 base64 内联在 JSONL 里的，
// 两者都可能很大；不设上限的话一条恶意/异常记录就能把响应撑爆。
const (
	MaxThinkingChars = 512 * 1024
	MaxLazyImageByte = 10 << 20 // 与 Pi Web 的 MAX_TOOL_RESULT_IMAGE_BYTES 一致
)

// lazyImageMimes 与 Pi Web 的 TOOL_RESULT_IMAGE_MIMES 对齐。
// 只收各浏览器都认的通用格式；SVG 不收——它是可执行内容。
var lazyImageMimes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true,
	"image/gif": true, "image/bmp": true, "image/avif": true,
}

// LazyBlock 描述一条记录里可延后加载的内容块。
type LazyBlock struct {
	// BlockIndex 是 content 数组下标，与 Pi Web 的 ?blockIndex= 一致。
	BlockIndex int
	// Kind 是 "thinking" 或 "image"。
	Kind string
}

// scanLazyBlocks 扫一条原始记录的 content，列出可延后加载的块。
// 只在投影阶段调用一次：历史页因此能带上占位符，而不必把内容本身传出去。
func scanLazyBlocks(message json.RawMessage) []LazyBlock {
	if len(message) == 0 {
		return nil
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &msg) != nil || len(msg.Content) == 0 {
		return nil
	}
	// content 可能是字符串（纯文本消息），那种情况没有可延后加载的块。
	// 用首字节判断形状，不靠 unmarshal 失败去试错。
	if shapeOf(msg.Content) != shapeArray {
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return nil
	}
	out := make([]LazyBlock, 0, 4)
	for i, b := range blocks {
		switch {
		case msg.Role == "assistant" && b.Type == "thinking":
			out = append(out, LazyBlock{BlockIndex: i, Kind: "thinking"})
		case (msg.Role == "toolResult" || msg.Role == "user") && b.Type == "image":
			out = append(out, LazyBlock{BlockIndex: i, Kind: "image"})
		}
	}
	return out
}

// Thinking 取某条 assistant 消息里指定下标的思考块文本。
//
// 历史页只带占位符：把几千字思考塞进每一页，翻页成本全花在
// 用户当时并没看的内容上。点开展开时才真正读取。
func (s *Store) Thinking(ctx context.Context, id, entryID string, blockIndex int) (string, error) {
	if blockIndex < 0 || blockIndex > 4096 {
		return "", protocol.E("invalid_params", "blockIndex 超出范围")
	}
	raw, role, err := s.rawEntry(ctx, id, entryID)
	if err != nil {
		return "", err
	}
	if role != "assistant" {
		return "", protocol.E("not_found", "条目不是助手消息")
	}
	var msg struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return "", protocol.E("pi_error", "条目内容解析失败")
	}
	if blockIndex >= len(msg.Content) {
		return "", protocol.E("not_found", "内容块不存在")
	}
	block := msg.Content[blockIndex]
	if block.Type != "thinking" {
		return "", protocol.E("not_found", "该内容块不是思考块")
	}
	if len(block.Thinking) > MaxThinkingChars {
		return block.Thinking[:MaxThinkingChars], nil
	}
	return block.Thinking, nil
}

// ToolImage 取某条 toolResult 消息里指定下标的图片，返回字节与 MIME。
func (s *Store) ToolImage(ctx context.Context, id, entryID string, blockIndex int) ([]byte, string, error) {
	return s.entryImage(ctx, id, entryID, blockIndex, "toolResult")
}

// UserImage 取用户附带的图片，不通过工具结果入口跨角色读取。
func (s *Store) UserImage(ctx context.Context, id, entryID string, blockIndex int) ([]byte, string, error) {
	return s.entryImage(ctx, id, entryID, blockIndex, "user")
}

func (s *Store) entryImage(ctx context.Context, id, entryID string, blockIndex int, expectedRole string) ([]byte, string, error) {
	if blockIndex < 0 || blockIndex > 4096 {
		return nil, "", protocol.E("invalid_params", "blockIndex 超出范围")
	}
	raw, role, err := s.rawEntry(ctx, id, entryID)
	if err != nil {
		return nil, "", err
	}
	if role != expectedRole {
		return nil, "", protocol.E("not_found", "条目角色与图片类型不符")
	}
	var msg struct {
		Content []struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			Source   struct {
				Type      string `json:"type"`
				Data      string `json:"data"`
				MediaType string `json:"media_type"`
			} `json:"source"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return nil, "", protocol.E("pi_error", "条目内容解析失败")
	}
	if blockIndex >= len(msg.Content) {
		return nil, "", protocol.E("not_found", "内容块不存在")
	}
	block := msg.Content[blockIndex]
	if block.Type != "image" {
		return nil, "", protocol.E("not_found", "该内容块不是图片")
	}
	// 兼容两种形状：平铺的 {data, mimeType} 与 Anthropic 的 {source:{...}}。
	data, mime := block.Data, block.MimeType
	if data == "" && block.Source.Type == "base64" {
		data, mime = block.Source.Data, block.Source.MediaType
	}
	if mime == "" || !lazyImageMimes[strings.ToLower(strings.SplitN(mime, ";", 2)[0])] {
		return nil, "", protocol.E("unsupported", "图片格式不受支持")
	}
	// 先按 base64 文本长度拦一道，避免为超大 payload 先分配内存。
	if len(data) > (MaxLazyImageByte*4)/3+8 {
		return nil, "", protocol.E("limit_exceeded", "图片超过体积上限")
	}
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		bytes, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
		if err != nil {
			return nil, "", protocol.E("pi_error", "图片不是合法 base64")
		}
	}
	if len(bytes) == 0 || len(bytes) > MaxLazyImageByte {
		return nil, "", protocol.E("limit_exceeded", "图片超过体积上限")
	}
	return bytes, strings.ToLower(strings.SplitN(mime, ";", 2)[0]), nil
}

// rawEntry 读一条原始记录并返回它的 message 与 role。
//
// 快路径复用 History 的扫描索引：里面已存好每条记录的 offset/size，
// 直接 ReadAt 单条即可。老实现每次都从文件头逐行扫到目标条目，
// 展开多个旧思考块会把长会话反复扫很多遍（B38/O03）。
// 缓存未命中（冷路径）就先做一次完整扫描并写入缓存。
//
// 扫描方式与 History 完全一致：bufio + jsonl.Read，按行边界切分，
// 只读到文件当前长度。这里曾手写过一套 chunk 扫描，结果 offset 在
// 消耗会话头后被推进两次，跨缓冲区的行既被跳过又被截断——单元测试
// 用小文件碰不到，真实会话里稍大的条目就整条找不到。
func (s *Store) rawEntry(ctx context.Context, id, entryID string) (json.RawMessage, string, error) {
	h, err := s.Find(ctx, id)
	if err != nil {
		return nil, "", err
	}
	f, err := s.root.Open(h.path)
	if err != nil {
		return nil, "", protocol.E("not_found", "会话文件不存在")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, "", protocol.E("pi_error", "无法读取会话文件")
	}
	if !st.Mode().IsRegular() || st.Size() > s.limits.FileBytes {
		return nil, "", fileTooLargeError("历史文件", st.Size(), s.limits.FileBytes)
	}
	// 与 History 用同一个缓存键（绝对路径），两边才能互相命中。
	nodes, _, err := s.scanNodes(ctx, h, f, st.Size(), st.ModTime().UnixNano())
	if err != nil {
		return nil, "", err
	}
	if target, ok := nodes[entryID]; ok && target.size > 0 {
		buf := make([]byte, target.size)
		if _, err := f.ReadAt(buf, target.offset); err == nil || errors.Is(err, io.EOF) {
			// 偏移可能因并发改写而错位：读出来的记录必须与请求的 ID 一致，
			// 否则回退到线性扫描，绝不给错内容。
			if msg, role, ok := entryMessage(buf, entryID); ok {
				return msg, role, nil
			}
		}
	}
	// 回退：线性扫描（保留原有更严格的错误报告）。
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", protocol.E("pi_error", "无法读取会话文件")
	}
	return s.rawEntryScan(ctx, f, st.Size(), entryID)
}

// entryMessage 从一条完整记录里取 message 与角色；ID 不符或无法解析时返回 false。
func entryMessage(line []byte, entryID string) (json.RawMessage, string, bool) {
	var probe struct {
		ID      string          `json:"id"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &probe) != nil || probe.ID != entryID || len(probe.Message) == 0 {
		return nil, "", false
	}
	var msg struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(probe.Message, &msg) != nil {
		return nil, "", false
	}
	role := msg.Role
	if role == "" {
		var cmd struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(probe.Message, &cmd) == nil && cmd.Command != "" {
			role = "toolResult"
		}
	}
	return probe.Message, role, true
}

// rawEntryScan 是兼容路径：从文件头逐行找到目标条目。
func (s *Store) rawEntryScan(ctx context.Context, f *os.File, size int64, entryID string) (json.RawMessage, string, error) {
	r := bufio.NewReader(io.LimitReader(f, size))
	headerSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		b, _, e := jsonl.Read(r, s.limits.LineBytes)
		if errors.Is(e, io.EOF) || errors.Is(e, jsonl.ErrIncomplete) {
			break
		}
		if e != nil {
			return nil, "", protocol.E("limit_exceeded", "历史记录超过体积上限")
		}
		if !headerSeen {
			headerSeen = true
			continue
		}
		var probe struct {
			ID      string          `json:"id"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(b, &probe) != nil || probe.ID == "" {
			continue
		}
		if probe.ID != entryID {
			continue
		}
		var msg struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(probe.Message, &msg) != nil {
			return nil, "", protocol.E("pi_error", "条目消息解析失败")
		}
		role := msg.Role
		if role == "" {
			var cmd struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(probe.Message, &cmd) == nil && cmd.Command != "" {
				role = "toolResult"
			}
		}
		return probe.Message, role, nil
	}
	return nil, "", protocol.E("not_found", "条目不存在")
}
