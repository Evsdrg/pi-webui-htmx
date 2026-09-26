package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"pi-bridge-go/internal/jsonl"
	"strings"
	"unicode"
)

// titleForPage 仅为当前列表页读取标题；不保存正文，不启动 Pi。
// Pi 把重命名写成 session_info，不能只读取第一行 session 头。
func (x *Index) titleForPage(ctx context.Context, e indexEntry) (indexEntry, error) {
	if e.titleRead || e.size > x.limits.FileBytes {
		return e, nil
	}
	f, err := x.root.Open(e.path)
	if err != nil {
		return e, nil
	}
	defer f.Close()
	reader := bufio.NewReader(io.LimitReader(f, e.size))
	var firstText string
	for count := 0; count < x.limits.Entries; count++ {
		if err := ctx.Err(); err != nil {
			return e, err
		}
		line, _, err := jsonl.Read(reader, x.limits.LineBytes)
		if errors.Is(err, io.EOF) || errors.Is(err, jsonl.ErrIncomplete) {
			break
		}
		if err != nil {
			return e, nil
		}
		if !bytes.Contains(line, []byte("\"session_info\"")) && (firstText != "" || !bytes.Contains(line, []byte("\"user\""))) {
			continue
		}
		var row struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if row.Type == "session_info" {
			e.name = shortTitle(row.Name, 160)
		}
		if firstText == "" && row.Type == "message" && row.Message.Role == "user" {
			var text string
			if json.Unmarshal(row.Message.Content, &text) == nil {
				firstText = shortTitle(text, 80)
			} else {
				var parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(row.Message.Content, &parts) == nil {
					for _, part := range parts {
						if part.Type == "text" {
							firstText = shortTitle(part.Text, 80)
							if firstText != "" {
								break
							}
						}
					}
				}
			}
		}
	}
	if e.name == "" {
		e.name = firstText
	}
	e.titleRead = true
	x.mu.Lock()
	if current, ok := x.entries[e.id]; ok && current.path == e.path && current.size == e.size && current.modified.Equal(e.modified) {
		x.entries[e.id] = e
	}
	x.mu.Unlock()
	return e, nil
}

func shortTitle(value string, limit int) string {
	var out strings.Builder
	count := 0
	space := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			space = out.Len() > 0
			continue
		}
		if count >= limit {
			out.WriteRune('…')
			break
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
		count++
	}
	return out.String()
}
