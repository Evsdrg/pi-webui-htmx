package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// 列表页的标题来自文件两端，而不是全文件扫描（B37）。
//
// 老实现为每条当前页会话从文件头扫到尾，只为找「最新一次 session_info」；
// 多个长会话时列表请求会重复读取大量完整 JSONL。事实上：
//   - 第一条用户消息总在头部附近；
//   - 最新一次重命名写在尾部附近（重命名之后新追加的消息不会改写它）。
//
// 所以头部有界读一次，尾部反向按块找一次即可。

const (
	// titleHeadBytes 是头部读取窗口：第一条用户消息一定在这段之内
	// （前面只有会话头与少量记录）。窗口内取不到就退回空标题——
	// 不为了一个标题把整个文件读完。
	titleHeadBytes = 64 << 10

	// titleTailChunk 是尾部反向读取的块大小，titleTailOverlap 是相邻块的重叠。
	// 重叠保证「跨块边界的行」在某一轮里被完整读到——只要行长不超过重叠量。
	// session_info 记录只有几百字节，远小于重叠量。
	titleTailChunk   = 64 << 10
	titleTailOverlap = 4 << 10

	// titleTailChunks 限制反向最多读多少块（默认 64 块 ≈ 4 MiB）。
	// 超过就放弃找名字，退回第一条用户文本。
	titleTailChunks = 64
)

// firstUserText 从文件头部找第一条用户消息的文本。
func firstUserText(f io.ReaderAt, size int64) (string, bool) {
	n := int64(titleHeadBytes)
	if size < n {
		n = size
	}
	if n <= 0 {
		return "", false
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	// 只处理完整行：窗口末尾可能截断一行，交给 scanFirstLine 跳过。
	return scanFirstLine(buf, func(line []byte) (string, bool) {
		if !bytes.Contains(line, []byte(`"user"`)) {
			return "", false
		}
		var row struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil || row.Type != "message" || row.Message.Role != "user" {
			return "", false
		}
		return contentText(row.Message.Content)
	})
}

// contentText 取消息正文里的纯文本（形状由首字节判断）。
func contentText(content json.RawMessage) (string, bool) {
	switch shapeOf(content) {
	case shapeString:
		var text string
		if json.Unmarshal(content, &text) == nil && text != "" {
			return text, true
		}
	case shapeArray:
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &parts) == nil {
			for _, part := range parts {
				if part.Type == "text" && part.Text != "" {
					return part.Text, true
				}
			}
		}
	}
	return "", false
}

// lastSessionInfoName 从尾部反向找最新一次 session_info 的名字。
// 每块内部取最后一条命中：反向块序 + 块内取最后 = 全文件里最新的一条。
func lastSessionInfoName(f io.ReaderAt, size int64) (string, bool) {
	if size <= 0 {
		return "", false
	}
	end := size
	for i := 0; i < titleTailChunks && end > 0; i++ {
		start := end - titleTailChunk
		if start < 0 {
			start = 0
		}
		readStart := start - titleTailOverlap
		if readStart < 0 {
			readStart = 0
		}
		buf := make([]byte, end-readStart)
		if _, err := f.ReadAt(buf, readStart); err != nil && !errors.Is(err, io.EOF) {
			return "", false
		}
		if name, ok := scanLastLine(buf, sessionInfoName); ok {
			return name, true
		}
		end = start
	}
	return "", false
}

// sessionInfoName 从一行里取 session_info 的名字。
func sessionInfoName(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"session_info"`)) {
		return "", false
	}
	var row struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(line, &row) != nil || row.Type != "session_info" || row.Name == "" {
		return "", false
	}
	return row.Name, true
}

// scanFirstLine 逐行扫描，返回第一条命中。
// 只接受以换行结束的行：末尾的半行可能是被窗口截断的记录。
func scanFirstLine(buf []byte, fn func(line []byte) (string, bool)) (string, bool) {
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		line := buf[:i]
		buf = buf[i+1:]
		if len(line) == 0 {
			continue
		}
		if value, ok := fn(line); ok {
			return value, true
		}
	}
	return "", false
}

// scanLastLine 逐行扫描，返回最后一条命中。
// 同样只接受完整行：Pi 正在追加时最后一行可能没有换行，
// 把它当成有效记录会读到一个尚未写完的名字。
func scanLastLine(buf []byte, fn func(line []byte) (string, bool)) (string, bool) {
	value, found := "", false
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		line := buf[:i]
		buf = buf[i+1:]
		if len(line) == 0 {
			continue
		}
		if hit, ok := fn(line); ok {
			value, found = hit, true
		}
	}
	return value, found
}
