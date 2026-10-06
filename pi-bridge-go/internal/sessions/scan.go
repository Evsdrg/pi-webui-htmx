package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
)

// scanFile 全文件扫描并建出 parent 链索引。
//
// 抽出来是为了让 History 能先查缓存：命中就完全跳过这里。
// 出错时返回 (nil, err)，错误与原先内联在 History 里的完全一致。
//
// 更常用的入口是 scanTail（只扫尾部窗口）。这里是它的退路：
// 窗口不够用（要看的页比窗口更早）时才走全扫。
func (s *Store) scanFile(ctx context.Context, f *os.File, size int64, id, cwd string) (map[string]node, string, error) {
	r := bufio.NewReader(io.LimitReader(f, size))
	// 复用缓冲：本循环只在当次迭代内用 b（存的是 offset/size），
	// 不把它交给任何人，因此复用是安全的。
	var reader jsonl.Reusable
	nodes := map[string]node{}
	offset := int64(0)
	last := ""
	headerSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		b, n, e := reader.Read(r, s.limits.LineBytes)
		if errors.Is(e, io.EOF) || errors.Is(e, jsonl.ErrIncomplete) {
			break
		}
		if e != nil {
			return nil, "", protocol.E("limit_exceeded",
				fmt.Sprintf("历史记录超过单行 %s 上限", humanBytes(int64(s.limits.LineBytes))))
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
		// 头部之后的空行按「没有记录」跳过，与尾扫一致（tail.go 的反向切行
		// 对空行直接 continue）。两条扫描路径必须等价，否则同一份文件会出现
		// 「首页能开、翻页报错」这类难查的不一致。
		if len(b) == 0 {
			offset += int64(n)
			continue
		}
		ref, err := parseRecordRef(b)
		if err != nil {
			return nil, "", err
		}
		if _, ok := nodes[ref.id]; ok {
			return nil, "", protocol.E("invalid_history", "历史条目 ID 重复")
		}
		// 正向扫描时父亲一定已经出现过，因此父链完整性可以在这里判：
		// 缺父亲或自指都是损坏。反向窗口扫描做不到这一点（窗口是截断的）。
		if ref.parent != "" {
			if _, ok := nodes[ref.parent]; !ok || ref.parent == ref.id {
				return nil, "", protocol.E("invalid_history", "父链断裂或存在环")
			}
		}
		if len(nodes) >= s.limits.Entries {
			return nil, "", protocol.E("limit_exceeded",
				fmt.Sprintf("历史条目数超过 %d 条上限", s.limits.Entries))
		}
		modelID := ""
		if ref.parent != "" {
			modelID = nodes[ref.parent].lastModelID
		}
		if ref.isModelChange {
			modelID = ref.id
		}
		nodes[ref.id] = node{parent: ref.parent, offset: offset, size: n, lastModelID: modelID, isUser: ref.isUser}
		last = ref.id
		offset += int64(n)
	}
	if !headerSeen {
		return nil, "", protocol.E("invalid_history", "缺少完整的会话头部")
	}
	return nodes, last, nil
}
