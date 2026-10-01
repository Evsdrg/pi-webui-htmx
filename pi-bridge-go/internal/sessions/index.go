package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/workspace"
)

// fnv64a 是轻量指纹累加器，避免为指纹引入额外依赖。
type fnv64a struct{ state uint64 }

func (f *fnv64a) Sum(b []byte) []byte {
	out := make([]byte, 8)
	v := f.state
	if v == 0 {
		v = 14695981039346656037
	}
	for i := 0; i < 8; i++ {
		out[i] = byte(v >> (8 * uint(i)))
	}
	return out
}

func writeFingerprint(h *fnv64a, path string, size int64, mod time.Time) {
	const offset = 14695981039346656037
	const prime = 1099511628211
	if h.state == 0 {
		h.state = offset
	}
	for i := 0; i < len(path); i++ {
		h.state ^= uint64(path[i])
		h.state *= prime
	}
	h.state ^= uint64(size)
	h.state *= prime
	h.state ^= uint64(mod.UnixNano())
	h.state *= prime
}

// indexEntry 是索引中的一条会话元数据，只保留渲染列表所需字段。
type indexEntry struct {
	id        string
	path      string // 相对 root 的斜杠路径
	cwd       string
	name      string
	titleRead bool
	version   int
	timestamp string
	modified  time.Time
	size      int64
}

// Index 是会话目录的内存索引。
// 设计约束：
//   - 只缓存元数据，不缓存正文，内存上限由 limits.Files 决定；
//   - 用「只遍历目录项、不打开文件」的指纹做失效判断，
//     既覆盖嵌套子目录，也避免每次请求都读几千个会话头；
//   - 会话数超过上限时保留最新的 limits.Files 项并标记 truncated，
//     既不隐藏数据也不无限增长。
type Index struct {
	root   *os.Root
	dir    string
	policy *workspace.Policy
	limits Limits
	ttl    time.Duration

	mu          sync.RWMutex
	entries     map[string]indexEntry
	order       []string // 按 modified 倒序的会话 ID
	builtAt     time.Time
	fingerprint string
	truncated   bool
	// stampFiles/stampMod 是顶层目录的轻量戳：TTL 内的有效性只靠它判断，
	// 不必遍历整棵树（B29）。
	stampFiles int
	stampMod   time.Time
}

// NewIndex 构造索引；root 必须已由 Store 打开。
func NewIndex(root *os.Root, dir string, policy *workspace.Policy, limits Limits, ttl time.Duration) *Index {
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	return &Index{root: root, dir: dir, policy: policy, limits: limits, ttl: ttl, entries: map[string]indexEntry{}}
}

// fingerprint 只遍历目录项并汇总路径、大小、修改时间，
// 不打开任何文件；嵌套子目录同样被覆盖。
func (x *Index) computeFingerprint(ctx context.Context) (string, error) {
	h := &fnv64a{}
	err := walkDir(ctx, x.root, ".", 0, func(path string, size int64, mod time.Time) error {
		writeFingerprint(h, path, size, mod)
		return nil
	})
	if err != nil {
		return "", err
	}
	return string(h.Sum(nil)), nil
}

// fresh 判断缓存是否仍可用：TTL 与指纹都要通过。
// fresh 判断缓存索引是否仍然有效。
//
// TTL 内只做轻量校验：比较顶层目录的 mtime 与 .jsonl 文件数。
// 旧实现在 TTL 内仍遍历整棵树计算指纹，大目录下每次列表都是
// O(会话文件数) 的开销（B29）。顶层 mtime 足以捕捉增删与改名；
// 追加写入由 size 变化在 build 阶段兜底——那本来就要重读。
func (x *Index) fresh(now time.Time) bool {
	x.mu.RLock()
	built := x.builtAt
	x.mu.RUnlock()
	if now.Sub(built) > x.ttl {
		return false
	}
	files, latest, err := x.topLevelStamp()
	if err != nil {
		return false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return files == x.stampFiles && latest.Equal(x.stampMod)
}

// build 全量重建索引。只读，不修改任何文件。
func (x *Index) build(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.entries) > 0 && time.Since(x.builtAt) <= x.ttl {
		if got, err := x.computeFingerprint(ctx); err == nil && got == x.fingerprint {
			return nil
		}
	}
	found := make([]indexEntry, 0, 256)
	walked := 0
	err := walkDir(ctx, x.root, ".", 0, func(path string, size int64, mod time.Time) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		walked++
		if walked > x.limits.Files*8 {
			return protocol.E("limit_exceeded", "会话目录遍历次数超过上限")
		}
		header, err := readHeader(x.root, path)
		if err != nil {
			// 正在写入或非会话文件直接跳过，不让单个坏文件拖垮整个列表。
			return nil
		}
		if _, err := x.policy.Directory(header.cwd); err != nil {
			return nil
		}
		if header.version != 3 {
			return nil
		}
		if cached, ok := x.entries[header.id]; ok && cached.path == path && cached.size == size && cached.modified.Equal(mod) {
			found = append(found, cached)
			return nil
		}
		found = append(found, indexEntry{
			id: header.id, path: path, cwd: header.cwd, name: header.name,
			version: header.version, timestamp: header.timestamp,
			modified: mod, size: size,
		})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].modified.Equal(found[j].modified) {
			return found[i].id < found[j].id
		}
		return found[i].modified.After(found[j].modified)
	})
	// 重复 ID 必须覆盖全部文件，不能只检查进入上限的那一批，
	// 否则超限时会悄悄选定其中一个副本。
	seen := make(map[string]struct{}, len(found))
	for _, e := range found {
		if _, dup := seen[e.id]; dup {
			return protocol.E("conflict", "配置目录内出现重复会话 ID")
		}
		seen[e.id] = struct{}{}
	}
	entries := make(map[string]indexEntry, len(found))
	order := make([]string, 0, len(found))
	truncated := false
	for i, e := range found {
		if i >= x.limits.Files {
			truncated = true
			break
		}
		entries[e.id] = e
		order = append(order, e.id)
	}
	x.entries = entries
	x.order = order
	x.truncated = truncated
	x.builtAt = time.Now()
	x.fingerprint, _ = x.computeFingerprint(ctx)
	// 轻量戳必须与索引一起更新，否则 fresh() 永远失配、
	// 每次列表都会退化成全量重建——那正是 B29 要消除的开销。
	if files, mod, serr := x.topLevelStamp(); serr == nil {
		x.stampFiles, x.stampMod = files, mod
	}
	return nil
}

// Invalidate 强制下次访问重建索引，用于文件被外部增删之后。
func (x *Index) Invalidate() {
	x.mu.Lock()
	x.builtAt = time.Time{}
	x.fingerprint = ""
	x.mu.Unlock()
}

// Refresh 确保索引可用，返回是否发生了截断。
func (x *Index) Refresh(ctx context.Context) (bool, error) {
	if x.fresh(time.Now()) {
		x.mu.RLock()
		defer x.mu.RUnlock()
		return x.truncated, nil
	}
	if err := x.build(ctx); err != nil {
		return false, err
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.truncated, nil
}

// Lookup 按会话 ID 取元数据。
func (x *Index) Lookup(ctx context.Context, id string) (indexEntry, error) {
	if _, err := x.Refresh(ctx); err != nil {
		return indexEntry{}, err
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	e, ok := x.entries[id]
	if !ok {
		return indexEntry{}, protocol.E("not_found", "会话不存在")
	}
	return e, nil
}

// Page 返回一页会话元数据，按最近修改倒序。
// Page 返回一页索引条目；cwd 非空时只取该工作区的。
//
// 筛选发生在切片之前：offset 与 hasMore 都相对于**筛选后**的集合，
// 否则「加载更多」会重复或跳过条目。会话数量级在百、千以内，
// 这里线性过滤足够，不值得为它维护每个 cwd 的独立索引。
func (x *Index) Page(ctx context.Context, offset, limit int, cwd string) ([]indexEntry, bool, bool, error) {
	truncated, err := x.Refresh(ctx)
	if err != nil {
		return nil, false, false, err
	}
	x.mu.RLock()
	ids := x.order
	if cwd != "" {
		ids = make([]string, 0, len(x.order))
		for _, id := range x.order {
			if x.entries[id].cwd == cwd {
				ids = append(ids, id)
			}
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(ids) {
		offset = len(ids)
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	out := make([]indexEntry, 0, end-offset)
	for _, id := range ids[offset:end] {
		out = append(out, x.entries[id])
	}
	hasMore := end < len(ids)
	x.mu.RUnlock()
	for i := range out {
		var err error
		out[i], err = x.titleForPage(ctx, out[i])
		if err != nil {
			return nil, false, false, err
		}
	}
	return out, hasMore, truncated, nil
}

// PathsForCwd 返回某个工作区下所有会话的相对路径集合，供搜索按工作区过滤。
// 路径形式与 walkDir 给出的一致（相对 root、斜杠分隔），可直接当集合键。
func (x *Index) PathsForCwd(ctx context.Context, cwd string) (map[string]bool, error) {
	if _, err := x.Refresh(ctx); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	x.mu.RLock()
	for _, e := range x.entries {
		if e.cwd == cwd {
			out[e.path] = true
		}
	}
	x.mu.RUnlock()
	return out, nil
}

// Workspaces 返回已知工作区及各自的会话数，按数量倒序（同数按路径序）。
//
// 空 cwd 的会话（极老或被手改过的文件）不计入任何工作区，它们仍能被
// 「全部」看到——这里只提供筛选选项。
func (x *Index) Workspaces(ctx context.Context) ([]CwdCount, error) {
	if _, err := x.Refresh(ctx); err != nil {
		return nil, err
	}
	x.mu.RLock()
	counts := map[string]int{}
	for _, e := range x.entries {
		if e.cwd != "" {
			counts[e.cwd]++
		}
	}
	x.mu.RUnlock()
	out := make([]CwdCount, 0, len(counts))
	for cwd, n := range counts {
		out = append(out, CwdCount{Cwd: cwd, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Cwd < out[j].Cwd
	})
	return out, nil
}

// Stats 返回索引规模，用于诊断。
func (x *Index) Stats() map[string]any {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return map[string]any{
		"sessions":  len(x.entries),
		"builtAt":   x.builtAt.UTC().Format(time.RFC3339Nano),
		"truncated": x.truncated,
		"ttl":       x.ttl.String(),
	}
}

// walkDir 遍历 root 下的会话文件，跳过符号链接与非普通文件。
// walkDir 流式遍历受管会话目录，只把 .jsonl 普通文件交给 fn。
//
// 流式是刻意的：旧实现用 ReadDir 一次性取回整个目录，超大单目录
// 会在上限检查之前就占满内存（B27/B52）。分批读取 + 目录数上限
// 保证遍历本身有界；ctx 让取消能及时中断（B52）。
func walkDir(ctx context.Context, root *os.Root, dir string, depth int, fn func(path string, size int64, mod time.Time) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > maxWalkDepth {
		return protocol.E("limit_exceeded", "会话目录层数超过上限")
	}
	f, err := root.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	for {
		items, rerr := f.ReadDir(256)
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := item.Name()
			path := name
			if dir != "." && dir != "" {
				path = dir + "/" + name
			}
			info, ierr := item.Info()
			if ierr != nil {
				continue
			}
			if item.IsDir() {
				if err := walkDir(ctx, root, path, depth+1, fn); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() || filepath.Ext(path) != ".jsonl" {
				continue
			}
			if err := fn(path, info.Size(), info.ModTime()); err != nil {
				return err
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			return rerr
		}
		if len(items) == 0 {
			return nil
		}
	}
}

// maxWalkDepth 限制遍历深度。会话目录按 cwd 编码成一层，
// 正常远小于此；异常深的目录会在这里被拦下，避免无界递归。
const maxWalkDepth = 32

// headerInfo 是会话头解析结果。
type headerInfo struct {
	id        string
	cwd       string
	name      string
	version   int
	timestamp string
}

// readHeader 只读首条记录，不改变文件。
func readHeader(root *os.Root, path string) (headerInfo, error) {
	f, err := root.Open(path)
	if err != nil {
		return headerInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return headerInfo{}, protocol.E("invalid_history", "不是普通文件")
	}
	b, _, err := jsonl.Read(bufio.NewReader(f), 64<<10)
	if err != nil {
		return headerInfo{}, err
	}
	var raw struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		Cwd       string `json:"cwd"`
		Name      string `json:"name"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return headerInfo{}, err
	}
	if raw.Type != "session" || !ValidID(raw.ID) {
		return headerInfo{}, protocol.E("invalid_history", "会话头部无效")
	}
	return headerInfo{id: raw.ID, cwd: raw.Cwd, name: raw.Name, version: raw.Version, timestamp: raw.Timestamp}, nil
}

// topLevelStamp 取顶层目录的 .jsonl 文件数与最新修改时间。
// 只看一层：会话目录按 cwd 编码成子目录，增删会话一定会改动顶层或子目录，
// 而子目录的改动会反映到它自身的 mtime 上——父目录 mtime 只在直接子项
// 增删时变化，因此这里同时比较文件数，追加写入由 build 阶段的 size 兜底。
func (x *Index) topLevelStamp() (int, time.Time, error) {
	f, err := x.root.Open(".")
	if err != nil {
		return 0, time.Time{}, err
	}
	defer f.Close()
	files := 0
	var latest time.Time
	for {
		items, rerr := f.ReadDir(256)
		for _, item := range items {
			info, ierr := item.Info()
			if ierr != nil {
				continue
			}
			if item.IsDir() {
				if info.ModTime().After(latest) {
					latest = info.ModTime()
				}
				continue
			}
			if !info.Mode().IsRegular() || filepath.Ext(item.Name()) != ".jsonl" {
				continue
			}
			files++
			if info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return files, latest, nil
			}
			return 0, time.Time{}, rerr
		}
		if len(items) == 0 {
			return files, latest, nil
		}
	}
}
