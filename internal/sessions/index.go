package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/workspace"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func newBufReader(f *os.File) *bufio.Reader { return bufio.NewReader(f) }

func fnvNew64a() *fnv64a { return &fnv64a{} }

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
func (x *Index) computeFingerprint() (string, error) {
	h := fnvNew64a()
	err := walkDir(x.root, ".", func(path string, size int64, mod time.Time) error {
		writeFingerprint(h, path, size, mod)
		return nil
	})
	if err != nil {
		return "", err
	}
	return string(h.Sum(nil)), nil
}

// fresh 判断缓存是否仍可用：TTL 与指纹都要通过。
func (x *Index) fresh(now time.Time) bool {
	x.mu.RLock()
	built := x.builtAt
	want := x.fingerprint
	x.mu.RUnlock()
	if now.Sub(built) > x.ttl {
		return false
	}
	got, err := x.computeFingerprint()
	if err != nil || got != want {
		return false
	}
	return true
}

// build 全量重建索引。只读，不修改任何文件。
func (x *Index) build(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.entries) > 0 && time.Since(x.builtAt) <= x.ttl {
		if got, err := x.computeFingerprint(); err == nil && got == x.fingerprint {
			return nil
		}
	}
	found := make([]indexEntry, 0, 256)
	walked := 0
	err := walkDir(x.root, ".", func(path string, size int64, mod time.Time) error {
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
	x.fingerprint, _ = x.computeFingerprint()
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
func (x *Index) Page(ctx context.Context, offset, limit int) ([]indexEntry, bool, bool, error) {
	truncated, err := x.Refresh(ctx)
	if err != nil {
		return nil, false, false, err
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	if offset < 0 {
		offset = 0
	}
	if offset > len(x.order) {
		offset = len(x.order)
	}
	end := offset + limit
	if end > len(x.order) {
		end = len(x.order)
	}
	out := make([]indexEntry, 0, end-offset)
	for _, id := range x.order[offset:end] {
		out = append(out, x.entries[id])
	}
	return out, end < len(x.order), truncated, nil
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
func walkDir(root *os.Root, dir string, fn func(path string, size int64, mod time.Time) error) error {
	items, err := root.FS().(interface {
		ReadDir(string) ([]os.DirEntry, error)
	}).ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, item := range items {
		name := item.Name()
		path := name
		if dir != "." && dir != "" {
			path = dir + "/" + name
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if item.IsDir() {
			if err := walkDir(root, path, fn); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if filepath.Ext(path) != ".jsonl" {
			continue
		}
		if err := fn(path, info.Size(), info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

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
	b, _, err := jsonl.Read(newBufReader(f), 64<<10)
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
	if err := jsonUnmarshal(b, &raw); err != nil {
		return headerInfo{}, err
	}
	if raw.Type != "session" || !ValidID(raw.ID) {
		return headerInfo{}, protocol.E("invalid_history", "会话头部无效")
	}
	return headerInfo{id: raw.ID, cwd: raw.Cwd, name: raw.Name, version: raw.Version, timestamp: raw.Timestamp}, nil
}
