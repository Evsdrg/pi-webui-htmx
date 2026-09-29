package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
)

// scanFile 全文件扫描并建出 parent 链索引。
//
// 抽出来是为了让 History 能先查缓存：命中就完全跳过这里。
// 出错时返回 (nil, err)，错误与原先内联在 History 里的完全一致。
func (s *Store) scanFile(ctx context.Context, f *os.File, size int64, id, cwd string) (map[string]node, string, error) {
	r := bufio.NewReader(io.LimitReader(f, size))
	nodes := map[string]node{}
	offset := int64(0)
	last := ""
	headerSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		b, n, e := jsonl.Read(r, s.limits.LineBytes)
		if errors.Is(e, io.EOF) || errors.Is(e, jsonl.ErrIncomplete) {
			break
		}
		if e != nil {
			return nil, "", protocol.E("limit_exceeded", "历史记录超过体积上限")
		}
		if !headerSeen {
			var current Header
			if json.Unmarshal(b, &current) != nil || current.Type != "session" || current.ID != id || current.Version != 3 || current.Cwd != cwd {
				return nil, "", protocol.E("conflict", "会话头部已变化")
			}
			headerSeen = true
			offset += int64(n)
			continue
		}
		// 快路径：只读行首三个字段。绝大多数记录在这里就结束，
		// 不必把整行（可能上百 KB）解析一遍。
		head, fast := parseEntryHead(b)
		if !fast {
			// 慢路径：完整解析。这里刻意自己校验并返回错误，
			// 而不是把结果塞进 entryHead——原来用 json.RawMessage
			// 承载 parentId，能区分「解析失败」和「解出来是空」；
			// 硬塞进 entryHead 会把「parentId 是数字」这类错误
			// 从「父条目 ID 无效」悄悄变成「父链断裂」。
			var item struct {
				Type   string          `json:"type"`
				ID     string          `json:"id"`
				Parent json.RawMessage `json:"parentId"`
				// Role 随同一次解析取出：message 的其余成员（可能是上百 KB
				// 的正文）会被跳过而不复制。
				Message struct {
					Role string `json:"role"`
				} `json:"message"`
			}
			if json.Unmarshal(b, &item) != nil || item.Type == "" || item.Type == "session" || !ValidID(item.ID) || len(item.Parent) == 0 {
				return nil, "", protocol.E("invalid_history", "完整的历史记录格式错误")
			}
			if _, ok := nodes[item.ID]; ok {
				return nil, "", protocol.E("invalid_history", "历史条目 ID 重复")
			}
			parent := ""
			if !bytes.Equal(item.Parent, []byte("null")) {
				if json.Unmarshal(item.Parent, &parent) != nil || !ValidID(parent) {
					return nil, "", protocol.E("invalid_history", "父条目 ID 无效")
				}
			}
			if parent != "" {
				if _, ok := nodes[parent]; !ok || parent == item.ID {
					return nil, "", protocol.E("invalid_history", "父链断裂或存在环")
				}
			}
			if len(nodes) >= s.limits.Entries {
				return nil, "", protocol.E("limit_exceeded", "历史索引条目数超过上限")
			}
			modelID := ""
			if parent != "" {
				modelID = nodes[parent].lastModelID
			}
			if item.Type == "model_change" {
				modelID = item.ID
			}
			nodes[item.ID] = node{parent: parent, offset: offset, size: n, lastModelID: modelID, isUser: item.Type == "message" && item.Message.Role == "user"}
			last = item.ID
			offset += int64(n)
			continue
		}
		if head.Type == "" || head.Type == "session" || !ValidID(head.ID) || !head.HasParent {
			return nil, "", protocol.E("invalid_history", "完整的历史记录格式错误")
		}
		// 快路径没看正文，必须补一次结构校验，否则正文损坏的记录会被静默接受（B13）。
		if !balancedJSON(b) {
			return nil, "", protocol.E("invalid_history", "完整的历史记录格式错误")
		}
		if _, ok := nodes[head.ID]; ok {
			return nil, "", protocol.E("invalid_history", "历史条目 ID 重复")
		}
		parent := head.Parent // parentId 为 null 时天然是空串
		if !head.ParentNull && !ValidID(parent) {
			return nil, "", protocol.E("invalid_history", "父条目 ID 无效")
		}
		if parent != "" {
			if _, ok := nodes[parent]; !ok || parent == head.ID {
				return nil, "", protocol.E("invalid_history", "父链断裂或存在环")
			}
		}
		if len(nodes) >= s.limits.Entries {
			return nil, "", protocol.E("limit_exceeded", "历史索引条目数超过上限")
		}
		modelID := ""
		if parent != "" {
			modelID = nodes[parent].lastModelID
		}
		if head.Type == "model_change" {
			modelID = head.ID
		}
		nodes[head.ID] = node{parent: parent, offset: offset, size: n, lastModelID: modelID, isUser: head.IsUser}
		last = head.ID
		offset += int64(n)
	}
	if !headerSeen {
		return nil, "", protocol.E("invalid_history", "缺少完整的会话头部")
	}
	return nodes, last, nil
}
