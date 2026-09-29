package sessions

import (
	"sync"
)

// scanCache 是单个会话文件的扫描结果缓存。
//
// 为什么值得缓存：History 每次请求都要把整个会话文件扫一遍才能建出
// parent 链。实测 29.3 MB / 40001 条的文件，「解析 + 建 map」占 54.9 ms，
// 而完整 History 是 69.6 ms——缓存掉这一步就是 79% 的收益。
// 用户连续翻页时这个差距会线性累积（翻 10 页就是 700 ms）。
//
// 失效判定只认 (size, mtime) 两项都完全一致：
//   - 用户只是翻页、切标签，文件不变 → 命中
//   - Pi 在追加写入 → size 变 →  miss，退回全扫
//   - Pi fork / 迁移触发原地重写 → size 或 mtime 至少一项变 → miss
//
// 刻意不做「增量续扫」：那需要假设已缓存的前缀未被改动，而
// migrateToCurrentVersion 会原地重写整个文件（可能缩小），
// 只靠 size 无法区分「追加」和「重写后恰好更大」。
// 宁可 miss 后全扫，也不拿正确性换这几十毫秒。
type scanCache struct {
	mu sync.Mutex
	// 只缓存最近访问的一个文件。翻页是严格串行的用户操作，
	// 多个会话同时翻页极少见；为这种情况维护 LRU 的复杂度不值。
	path  string
	size  int64
	mtime int64 // UnixNano
	// dev/ino 是文件身份。原子替换（rename）会换 inode，
	// 只靠 (path, size, mtime) 会被「同长度、保留 mtime 的替换」骗过（B12）。
	dev   uint64
	ino   uint64
	nodes map[string]node
	last  string
	// bytes 是 nodes 的粗略字节估计，用于有界控制与诊断。
	bytes int
}

// maxCachedNodes 限制单个缓存能占的内存。
// 100000 正是 DefaultLimits().Entries 的上限——超过它 History 本来就会拒绝，
// 所以缓存到这么大已经没有意义。
const maxCachedNodes = 100000

// maxCachedBytes 是缓存的内存上界。一个 node 约 40 字节实际占用
// （含 isUser 那个 bool），100000 条约 4 MB；这里给到 16 MB，留足 map 开销余量。
const maxCachedBytes = 16 << 20

// get 返回缓存的扫描结果。路径、大小、mtime 与文件身份全部匹配才算命中。
// 命中时返回的 map 归调用方只读，不得修改。
func (c *scanCache) get(path string, size, mtime int64) (map[string]node, string, bool) {
	dev, ino, ok := fileIdentity(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodes == nil || c.path != path || c.size != size || c.mtime != mtime {
		return nil, "", false
	}
	// 取不到文件身份时不能命中：宁可多扫一次，也不能用旧索引。
	if !ok || c.dev != dev || c.ino != ino {
		return nil, "", false
	}
	return c.nodes, c.last, true
}

// put 写入缓存。超过上界时不缓存，直接返回——
// 那种规模的会话 History 也会因 Entries 上限而拒绝，缓存无意义。
func (c *scanCache) put(path string, size, mtime int64, nodes map[string]node, last string) {
	if len(nodes) > maxCachedNodes {
		return
	}
	dev, ino, ok := fileIdentity(path)
	if !ok {
		// 取不到身份就不缓存，避免写入一条无法验证的条目。
		return
	}
	est := len(nodes) * 64 // 粗估：map 桶 + string 头 + 值
	if est > maxCachedBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path, c.size, c.mtime, c.dev, c.ino = path, size, mtime, dev, ino
	c.nodes, c.last, c.bytes = nodes, last, est
}

// stats 返回当前缓存的粗略规模，供诊断端点使用。
func (c *scanCache) stats() (nodes int, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.nodes), c.bytes
}
