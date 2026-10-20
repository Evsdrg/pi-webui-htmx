package sessions

import (
	"sync"
)

// scanCache 是会话扫描结果的小型多槽缓存。
//
// 为什么值得缓存：History 每次请求都要把整个会话文件扫一遍才能建出
// parent 链。实测 29.3 MB / 40001 条的文件，「解析 + 建 map」占 54.9 ms，
// 而完整 History 是 69.6 ms——缓存掉这一步就是 79% 的收益。
//
// 为什么要多个槽（O04）：实测 A（62.8 MiB）→ B（48.3 MiB）→ A，
// 单槽每次回到 A 都要重扫，约 112–131 ms；四槽原型约 10.5–10.9 ms。
// 翻页本身是串行的，但「两个会话之间来回看」是常见操作。
//
// 为什么缓存尾部窗口：首页只用到最近的几百条，而窗口扫描实测是
// 3.6 ms / 4 MiB（86.9 MB 的会话）——比全扫快 40 倍。缓存住它，
// 连续刷新、切换面板都不会重新读盘。
//
// 失效判定只认 size、mtime 与文件身份（dev/ino）全部一致：
//   - 用户只是翻页、切标签，文件不变 → 命中
//   - Pi 在追加写入 → size 变 →  miss，退回全扫
//   - Pi fork / 迁移触发原地重写 → size 或 mtime 至少一项变 → miss
//
// 刻意不做「增量续扫」：那需要假设已缓存的前缀未被改动，而
// migrateToCurrentVersion 会原地重写整个文件（可能缩小），
// 只靠 size 无法区分「追加」和「重写后恰好更大」。
// 宁可 miss 后全扫，也不拿正确性换这几十毫秒。
type scanCache struct {
	mu       sync.Mutex
	slots    []scanSlot
	capacity int
	// clock 是逻辑时钟，用来挑「最久未用」的槽。
	clock uint64
	bytes int
}

// scanSlot 是一格扫描结果。
type scanSlot struct {
	path  string
	size  int64
	mtime int64 // UnixNano
	// dev/ino 是文件身份。原子替换（rename）会换 inode，
	// 只靠 (path, size, mtime) 会被「同长度、保留 mtime 的替换」骗过（B12）。
	dev   uint64
	ino   uint64
	nodes map[string]node
	last  string
	// complete 表示这格索引覆盖了整个文件。false 时它只是尾部窗口，
	// 取页取到窗口边界就必须退回升到全扫（见 scanTail 的说明）。
	complete bool
	// capped 表示扫描按 Entries 条数封顶：索引只含最新的 Entries 条。
	// 取页到边界是「更早不可达」，不能退全扫（见 historyPage）。
	capped bool
	// bytes 是 nodes 的粗略字节估计，用于有界控制与诊断。
	bytes int
	used  uint64
}

const (
	// maxCachedNodes 限制单格缓存能占的节点数。
	// 与 DefaultLimits().Entries 的上限同量级：更大的封顶索引占内存太多，
	// 不值得常驻（16 MB 上限本身也会把它挡住）。
	maxCachedNodes = 100000

	// maxCachedBytes 是单格的内存上界。一个 node 约 40 字节实际占用
	// （含 isUser 那个 bool），100000 条约 4 MB；这里给到 16 MB，留足 map 开销余量。
	maxCachedBytes = 16 << 20

	// maxCachedTotalBytes 是所有格子的总预算。四格各自到 16 MB 会占 64 MB，
	// 对一台小机器太重；32 MB 是折中——常见会话只有几 MB，只有极大的会话才会被挡住。
	maxCachedTotalBytes = 32 << 20

	// scanCacheSlots 是默认格数。
	scanCacheSlots = 4
)

// slotCount 返回实际格数；零值 scanCache 用默认值。
func (c *scanCache) slotCount() int {
	if c.capacity <= 0 {
		return scanCacheSlots
	}
	return c.capacity
}

// get 返回缓存的扫描结果。路径、大小、mtime 与文件身份全部匹配才算命中。
// 命中时返回的 map 归调用方只读，不得修改。
// complete 表示这格索引是否覆盖整个文件。
func (c *scanCache) get(path string, size, mtime int64) (map[string]node, string, bool, bool, bool) {
	// 取不到文件身份时不能命中：宁可多扫一次，也不能用旧索引。
	dev, ino, ok := fileIdentity(path)
	if !ok {
		return nil, "", false, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.slots {
		s := &c.slots[i]
		if s.nodes == nil || s.path != path || s.size != size || s.mtime != mtime {
			continue
		}
		if s.dev != dev || s.ino != ino {
			continue
		}
		c.clock++
		s.used = c.clock
		return s.nodes, s.last, s.complete, s.capped, true
	}
	return nil, "", false, false, false
}

// drop 作废某个文件的缓存。
//
// 用于「按索引读出的字节与记录对不上」这种情形：缓存键（size/mtime/dev/ino）
// 全部命中却读错，说明文件被原地改写过而我们没察觉（Pi 的 fork 与版本迁移
// 都是 openSync(file,"w") 原地重写，若长度恰好不变、mtime 又落在同一纳秒，
// 现有判据看不见）。这时唯一正确的动作是丢掉这份索引重扫，而不是继续用它、
// 也不是把错误甩给用户。
func (c *scanCache) drop(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.slots[:0]
	for _, s := range c.slots {
		if s.path == path {
			c.bytes -= s.bytes
			continue
		}
		kept = append(kept, s)
	}
	c.slots = kept
}

// put 写入缓存。超过上界时不缓存，直接返回——超大会话的索引留在堆上
// 不受预算约束，风险与收益不成比例（封顶索引也一样按节点数把关）。
func (c *scanCache) put(path string, size, mtime int64, nodes map[string]node, last string, complete, capped bool) {
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
	c.clock++
	slot := scanSlot{path: path, size: size, mtime: mtime, dev: dev, ino: ino, nodes: nodes, last: last, complete: complete, capped: capped, bytes: est, used: c.clock}
	replaced := false
	for i := range c.slots {
		// 同一文件只占一格：尺寸变了就是新版本，替换掉旧的。
		if c.slots[i].path == path {
			c.bytes += est - c.slots[i].bytes
			c.slots[i] = slot
			replaced = true
			break
		}
	}
	if !replaced {
		if len(c.slots) < c.slotCount() {
			c.slots = append(c.slots, slot)
			c.bytes += est
		} else {
			victim := c.oldestLocked()
			c.bytes += est - c.slots[victim].bytes
			c.slots[victim] = slot
		}
	}
	// 总预算：必要时继续淘汰最久未用的格子。
	for c.bytes > maxCachedTotalBytes && len(c.slots) > 0 {
		victim := c.oldestLocked()
		c.bytes -= c.slots[victim].bytes
		c.slots = append(c.slots[:victim], c.slots[victim+1:]...)
	}
}

// oldestLocked 返回最久未使用格子的下标。调用方必须已持有锁且至少有一格。
func (c *scanCache) oldestLocked() int {
	victim := 0
	for i := range c.slots {
		if c.slots[i].used < c.slots[victim].used {
			victim = i
		}
	}
	return victim
}

// stats 返回当前缓存的粗略规模，供诊断端点使用。
func (c *scanCache) stats() (nodes int, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.slots {
		nodes += len(c.slots[i].nodes)
		bytes += c.slots[i].bytes
	}
	return nodes, bytes
}
