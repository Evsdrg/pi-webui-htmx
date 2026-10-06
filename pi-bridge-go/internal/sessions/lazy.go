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
	"unicode/utf8"

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
		// 按字节截断会切断多字节 UTF-8（中文/emoji 思考很常见），
		// 产生非法字节序列。回退到 rune 边界，与导出的 clipText 一致。
		return clipToRuneBoundary(block.Thinking, MaxThinkingChars), nil
	}
	return block.Thinking, nil
}

// clipToRuneBoundary 截到不超过 max 字节的最后一个完整 UTF-8 边界。
func clipToRuneBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
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
// 三段式路径：
//
//  1. 快路径按扫描索引的 offset/size 直接 ReadAt 单条。索引命中缓存或
//     目标在尾部窗口内时，展开任何条目都是单次磁盘读（B38/O03）。
//  2. 目标在尾部窗口之外（或窗口扫描失败）时全扫一次，把**完整**索引
//     写进缓存，再按偏移读。首次展开付一次全扫（与旧的逐次线性扫描同价），
//     此后任何位置的展开、以及往前翻很多页的历史取页都直接命中完整索引。
//  3. 全扫失败（文件含损坏行等）或读数校验不通过时，退回宽松的线性扫描。
//     它跳过无法解析的行——这正是 B38 之前的行为：损坏位置之前的条目仍然
//     可读，而不是把整份文件判死。窗口扫描对坏行是严格的，没有这条兜底，
//     一条坏行就会让全部展开请求失败。
//
// 读数一律先校验记录 ID 与请求一致，绝不给错内容：偏移可能因并发改写而错位。
//
// 这里不再手写 chunk 扫描：曾自研的一套 offset 在消耗会话头后被推进两次，
// 跨缓冲区的行既被跳过又被截断——单元测试用小文件碰不到，真实会话里稍大的
// 条目就整条找不到。扫描一律走与 History 相同的 bufio + jsonl.Read。
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
	mtime := st.ModTime().UnixNano()
	// 与 History 用同一个缓存键（绝对路径），两边才能互相命中。
	nodes, _, complete, scanErr := s.scanNodes(ctx, h, f, st.Size(), mtime, 1)
	if scanErr == nil {
		if target, ok := nodes[entryID]; ok && target.size > 0 {
			if msg, role, ok := s.readIndexed(f, target, entryID); ok {
				return msg, role, nil
			}
		} else if complete {
			// 完整索引里没有这个条目：与线性扫描得到同样的结论，且不必再扫。
			return nil, "", protocol.E("not_found", "条目不存在")
		}
	}
	// 窗口未命中：全扫一次建完整索引（失败说明文件有问题，交给线性兜底）。
	if scanErr == nil {
		if full, _, fullErr := s.scanFullAll(ctx, h, f, st.Size(), mtime); fullErr == nil {
			if target, ok := full[entryID]; ok {
				if msg, role, ok := s.readIndexed(f, target, entryID); ok {
					return msg, role, nil
				}
			} else {
				return nil, "", protocol.E("not_found", "条目不存在")
			}
		}
	}
	// 宽松线性扫描兜底。
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", protocol.E("pi_error", "无法读取会话文件")
	}
	return s.rawEntryScan(ctx, f, st.Size(), entryID)
}

// readIndexed 按索引读一条完整记录。读不到或记录 ID 与请求不符时返回 false，
// 由调用方决定兜底路径；绝不给错内容。
func (s *Store) readIndexed(f *os.File, target node, entryID string) (json.RawMessage, string, bool) {
	buf := make([]byte, target.size)
	if _, err := f.ReadAt(buf, target.offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, "", false
	}
	return entryMessage(buf, entryID)
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
