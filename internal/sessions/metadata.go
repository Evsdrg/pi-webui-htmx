package sessions

import (
	"context"
	"strings"
	"unicode"
)

// titleForPage 仅为当前列表页读取标题；不保存正文，不启动 Pi。
// Pi 把重命名写成 session_info，不能只读取第一行 session 头。
//
// 两条信息分别在文件两端：第一条用户消息在头部，最新一次重命名在尾部。
// 所以这里不再全文件扫描（B37）：头部有界读一次，尾部反向按块找一次。
// 全局扫描的等价物由会话目录索引的 TTL 缓存承担，不在这里重复。
func (x *Index) titleForPage(ctx context.Context, e indexEntry) (indexEntry, error) {
	if e.titleRead || e.size > x.limits.FileBytes {
		return e, nil
	}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	f, err := x.root.Open(e.path)
	if err != nil {
		return e, nil
	}
	defer f.Close()
	name := ""
	if value, ok := lastSessionInfoName(f, e.size); ok {
		name = shortTitle(value, 160)
	}
	if name == "" {
		if value, ok := firstUserText(f, e.size); ok {
			name = shortTitle(value, 80)
		}
	}
	e.name = name
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
