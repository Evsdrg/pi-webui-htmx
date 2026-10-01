package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"pi-bridge-go/internal/protocol"
)

// 尾部窗口的常量。
//
// 为什么值得只扫尾部：历史永远从最新的叶子往前取页，首页就是最近的一页。
// 实测一个 86.9 MB / 24489 条的真实会话：
//
//	全扫            154 ms   分配 8.4 MiB
//	只读尾部 1 MiB    0.9 ms   覆盖 310 行
//	只读尾部 4 MiB    3.6 ms   覆盖 718 行
//
// 相差 40 倍以上。窗口外的内容要往前翻几十页才会看到，那时才退化为全扫。
const (
	// tailWindowBytes 是窗口的目标大小，同时也是每次扩窗的步长。
	tailWindowBytes = 4 << 20
	// tailMaxBytes 是窗内条数仍然不够时的硬上限：再多就不值得为一次首页付代价。
	tailMaxBytes = 32 << 20
)

// scanTail 从文件尾部向前扫描，只建最近一段的索引。
//
// minNodes 是至少要有多少条索引（首页要用 limit 条，外加对齐轮边界的一点余量）。
// complete 为 true 表示窗口覆盖了整个文件，此时结果与 scanFile 在合法文件上等价。
//
// 实现上先把窗口一次读进内存再在内存里切行，而不是边读边切：实测发现
// 边读边切时，跨块的长行会让「行的结束位置」落在更晚的块里，切片越界
// （真实会话单行可达 112 KB，而块是 256 KB，跨界并不罕见）。
// 读进内存后切行没有边界特例。
//
// 与 scanFile 的三处刻意差异：
//
//   - **不做父链完整性校验**。反向扫描时父亲尚未解析到，窗口起点那条的父亲
//     本来就在窗口外，无法区分「正常截断」与「损坏」。ID 重复仍然检测。
//   - **窗口外的问题记录不会被发现**，要等翻到那里（退化为全扫）才报错。
//   - **lastModelID 在窗口最旧一段上可能为空**。这个值是「沿父链最近的
//     model_change」；若窗口内第一个 model_change 之前还有节点、而它们的
//     起点在窗口之外，就只能算成空。代价可接受：historicalModel 在
//     lastModelID 为空时本来就会退化为「从最近的助手回复取 provider/model」，
//     那是它处理旧会话的既有路径，不会显示成错误的模型。窗口覆盖全文时无此问题。
func (s *Store) scanTail(ctx context.Context, f *os.File, size int64, id, cwd string, minNodes int) (map[string]node, string, bool, error) {
	if size <= 0 {
		return nil, "", false, protocol.E("invalid_history", "缺少完整的会话头部")
	}
	// 有效末尾：忽略末尾那段没有换行的半行。Pi 正在追加时这是正常状态，
	// 与正向扫描遇到 jsonl.ErrIncomplete 就停的行为一致。
	end, err := effectiveEnd(f, size)
	if err != nil {
		return nil, "", false, err
	}
	if end <= 0 {
		return nil, "", false, protocol.E("invalid_history", "缺少完整的会话头部")
	}
	if minNodes < 1 {
		minNodes = 1
	}

	// 窗口从 4 MiB 起步；窗内完整条数不够就翻倍，直到文件开头或硬上限。
	window := int64(tailWindowBytes)
	if window > end {
		window = end
	}
	var buf []byte
	lo := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", false, err
		}
		lo = end - window
		if lo < 0 {
			lo = 0
		}
		buf = make([]byte, end-lo)
		if _, err := f.ReadAt(buf, lo); err != nil {
			return nil, "", false, err
		}
		if lo == 0 || countCompleteLines(buf, lo == 0) >= minNodes || window >= tailMaxBytes {
			break
		}
		window *= 4
	}
	complete := lo == 0

	type item struct {
		id            string
		isModelChange bool
	}
	nodes := make(map[string]node, minNodes+64)
	items := make([]item, 0, minNodes+64) // 逆序：最新在前
	headerSeen := false
	// 从后往前切行。lineEnd 是本行的结束位置（换行符下标或缓冲末尾）。
	lineEnd := len(buf)
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] != '\n' {
			continue
		}
		lineStart := i + 1
		if lineStart >= lineEnd {
			lineEnd = i
			continue
		}
		line := buf[lineStart:lineEnd]
		switch {
		case len(line) > s.limits.LineBytes:
			return nil, "", false, protocol.E("limit_exceeded",
				fmt.Sprintf("历史记录超过单行 %s 上限", humanBytes(int64(s.limits.LineBytes))))
		default:
			ref, err := parseRecordRef(line)
			if err != nil {
				return nil, "", false, err
			}
			if _, dup := nodes[ref.id]; dup {
				return nil, "", false, protocol.E("invalid_history", "历史条目 ID 重复")
			}
			if len(items) >= s.limits.Entries {
				return nil, "", false, protocol.E("limit_exceeded",
					fmt.Sprintf("历史条目数超过 %d 条上限", s.limits.Entries))
			}
			// size 含行尾换行符：与 scanFile 里 jsonl.Read 返回的长度口径一致，
			// 两边必须能逐字节比对（取页时按 size 分配并整块读出）。
			// 缓冲末尾一定是行边界（end 取自最后一个换行之后），所以这里 +1 成立。
			nodes[ref.id] = node{parent: ref.parent, offset: lo + int64(lineStart), size: len(line) + 1, isUser: ref.isUser}
			items = append(items, item{id: ref.id, isModelChange: ref.isModelChange})
		}
		lineEnd = i
	}
	// 第一行没有前导换行，上面的循环切不出来，单独处理。
	if lo == 0 && lineEnd > 0 {
		line := buf[:lineEnd]
		var head Header
		if json.Unmarshal(line, &head) != nil || head.Type != "session" || head.Version != 3 {
			return nil, "", false, protocol.E("invalid_history", "缺少完整的会话头部")
		}
		if head.ID != id || head.Cwd != cwd {
			return nil, "", false, protocol.E("conflict", "会话头部已变化")
		}
		headerSeen = true
	}
	if complete && !headerSeen {
		return nil, "", false, protocol.E("invalid_history", "缺少完整的会话头部")
	}
	if complete {
		// 窗口覆盖全文时能把父链完整校一遍，行为与 scanFile 一致：
		// 引用一个不存在的父亲就是损坏。窗口不完整时做不到这一点
		// （窗口起点那条的父亲本来就在窗口外）。
		// 自环也放在这里：反向扫描时「自环」与「重复 ID」可能在同一条
		// 记录上同时成立，先判重复才能给出与正向扫描一致的错误原因。
		for entryID, n := range nodes {
			if n.parent == entryID {
				return nil, "", false, protocol.E("invalid_history", "父链断裂或存在环")
			}
			if n.parent == "" {
				continue
			}
			if _, ok := nodes[n.parent]; !ok {
				return nil, "", false, protocol.E("invalid_history", "父链断裂或存在环")
			}
		}
	}
	if len(items) == 0 {
		return nil, "", false, protocol.E("invalid_history", "缺少完整的会话头部")
	}

	// 按文件顺序正向重算 lastModelID。反向扫描本身算不出它：这个值是
	// 「沿父链（更旧方向）最近的 model_change」，而反向遍历遇到的是相反方向。
	// items 是逆序的，所以从末位（最旧）走向首位（最新）。
	modelID := ""
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.isModelChange {
			modelID = it.id
		}
		n := nodes[it.id]
		n.lastModelID = modelID
		nodes[it.id] = n
	}
	return nodes, items[0].id, complete, nil
}

// countCompleteLines 数出缓冲里的完整行数。
//
// 缓冲末尾一定是行边界（end 取自最后一个换行之后），所以末尾不会产生半行。
// 缓冲开头若不是文件开头，则第一个换行之前那段是不完整的行（它的换行已计入），
// 要减一。
func countCompleteLines(buf []byte, atFileStart bool) int {
	n := 0
	for _, c := range buf {
		if c == '\n' {
			n++
		}
	}
	if !atFileStart && n > 0 {
		n--
	}
	return n
}

// effectiveEnd 返回忽略末尾半行之后的有效长度。
//
// 只在最后一块内找最后一个换行；找不到说明末端有超长行，那属于损坏文件，
// 由调用方按「缺少完整会话头部」处理。
func effectiveEnd(f *os.File, size int64) (int64, error) {
	const probe = 256 << 10
	n := int64(probe)
	if n > size {
		n = size
	}
	start := size - n
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, start); err != nil {
		return 0, err
	}
	for i := int(n) - 1; i >= 0; i-- {
		if buf[i] == '\n' {
			return start + int64(i) + 1, nil
		}
	}
	return 0, nil
}
