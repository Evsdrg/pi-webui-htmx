package workspace

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
)

// 索引上限。与 Pi Web 的 /api/file-index 对齐，但按桥的职责收紧：
// 无 query 时返回的「客户端索引」上限；walk 的硬上限；单次查询返回的匹配数。
const (
	MaxIndexFiles  = 5000
	MaxWalkFiles   = 50000
	MaxIndexDepth  = 8
	MaxIndexQuery  = 200
	MaxIndexMatch  = 50
	IndexCacheTTL  = 10 * time.Second
	IndexCacheSize = 20
	GitListTimeout = 10 * time.Second
)

// ignoredNames 与 Pi Web 的 /api/files 一致——只在非 git 的 walk 兜底里用。
// git 仓库靠 .gitignore，与 TUI 的 fd 行为一致。
var ignoredNames = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, "dist": true, "build": true,
	"__pycache__": true, ".turbo": true, ".cache": true, "coverage": true,
	".pytest_cache": true, ".mypy_cache": true, "target": true, "vendor": true,
	".DS_Store": true,
}

// IndexMatch 是一条排序后的匹配结果。
type IndexMatch struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

// IndexResult 是 files.index 的返回值。
// truncated 表示结果因输出预算、条目数或客户端索引上限不完整。
type IndexResult struct {
	Files     []string     `json:"files"`
	Matches   []IndexMatch `json:"matches,omitempty"`
	Truncated bool         `json:"truncated"`
}

type indexEntry struct {
	listing   []string
	byParent  map[string][]string
	expireAt  time.Time
	truncated bool
}

// Index 列出目录下的文件，供前端的 @ 补全使用。
//
// 有 query 时在服务端排序：@ 菜单每敲一个字都会请求一次，
// 把 5000 条原文拉回浏览器再过滤等于每次都传 100 KB 以上。
// 无 query 时返回截断后的列表，让前端可以本地兜底过滤。
func (f *Files) Index(ctx context.Context, path, query string) (IndexResult, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return IndexResult{}, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return IndexResult{}, err
	}
	info, err := r.Stat(rel)
	if err != nil {
		return IndexResult{}, protocol.E("not_found", "路径不存在")
	}
	if !info.IsDir() {
		return IndexResult{}, protocol.E("invalid_params", "目标不是目录")
	}
	if len(query) > MaxIndexQuery {
		query = query[:MaxIndexQuery]
	}

	entry, err := f.indexFor(ctx, root, rel)
	if err != nil {
		return IndexResult{}, err
	}

	if query == "" {
		files := entry.listing
		truncated := entry.truncated
		if len(files) > MaxIndexFiles {
			files = files[:MaxIndexFiles]
			truncated = true
		}
		return IndexResult{Files: files, Truncated: truncated}, nil
	}
	return IndexResult{Matches: rankMatches(entry.byParent, query), Truncated: entry.truncated}, nil
}

// indexFor 取缓存，过期则重建。缓存按根+相对路径分桶，TTL 与条数都有界。
func (f *Files) indexFor(ctx context.Context, root, rel string) (*indexEntry, error) {
	key := root + "\x00" + rel
	f.indexMu.Lock()
	if f.indexCache == nil {
		f.indexCache = map[string]*indexEntry{}
	}
	now := time.Now()
	if e, ok := f.indexCache[key]; ok && e.expireAt.After(now) {
		f.indexMu.Unlock()
		return e, nil
	}
	f.indexMu.Unlock()

	abs := filepath.Join(root, filepath.FromSlash(rel))
	listing, truncated, err := f.listIndex(ctx, root, rel, abs)
	if err != nil {
		return nil, err
	}
	entry := &indexEntry{listing: listing, byParent: groupByParent(listing), expireAt: now.Add(IndexCacheTTL), truncated: truncated}

	f.indexMu.Lock()
	defer f.indexMu.Unlock()
	// 顺手清掉过期项；条目数超限就整体清空。
	// 逐条 LRU 要维护访问时间戳，收益不抵簿记成本。
	for k, v := range f.indexCache {
		if v.expireAt.Before(now) {
			delete(f.indexCache, k)
		}
	}
	if len(f.indexCache) >= IndexCacheSize {
		f.indexCache = map[string]*indexEntry{}
	}
	f.indexCache[key] = entry
	return entry, nil
}

// listIndex 仅在不是仓库或未安装 Git 时退回 walk；拒绝与取消不能静默降级。
func (f *Files) listIndex(ctx context.Context, root, rel, abs string) ([]string, bool, error) {
	listing, truncated, err := f.listWithGit(ctx, abs)
	if !errors.Is(err, errNotGit) {
		return listing, truncated, err
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	listing, err = f.listWithWalk(root, rel)
	return listing, len(listing) >= MaxWalkFiles, err
}

// listWithGit 复用受控 runner，按 NUL 增量读取，字节与条目均有硬上限。
func (f *Files) listWithGit(ctx context.Context, abs string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, GitListTimeout)
	defer cancel()
	dir, err := f.gitRepository(ctx, abs)
	if err != nil {
		return nil, false, err
	}
	listing := make([]string, 0, 1024)
	parser := &nulRecords{consume: func(name string) bool {
		if name == "" {
			return true
		}
		if len(listing) >= MaxWalkFiles {
			return false
		}
		listing = append(listing, filepath.ToSlash(name))
		return true
	}}
	truncated, code, err := f.runGit(ctx, dir, gitMaxOutput, parser, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ".")
	if err != nil {
		return nil, false, err
	}
	if code != 0 {
		return nil, false, protocol.E("pi_error", "Git 文件索引查询失败")
	}
	return listing, truncated, nil
}

// listWithWalk 是 BFS 复制目录兜底。BFS 保证浅文件先入列，
// 触到硬上限时留下的仍是最可能被引用的那批。
// 走 os.Root 的 FS，符号链接不展开，根外内容进不来。
func (f *Files) listWithWalk(root, rel string) ([]string, error) {
	r, err := f.rootFor(root)
	if err != nil {
		return nil, err
	}
	listing := make([]string, 0, 1024)
	type item struct {
		path  string
		depth int
	}
	queue := []item{{path: rel, depth: 0}}
	truncated := false
	for len(queue) > 0 && !truncated {
		cur := queue[0]
		queue = queue[1:]
		entries, err := fs.ReadDir(r.FS(), cur.path)
		if err != nil {
			// 读不了的目录跳过：可能是权限，也可能是并发删除。
			continue
		}
		for _, e := range entries {
			if ignoredNames[e.Name()] {
				continue
			}
			child := e.Name()
			if cur.path != "" && cur.path != "." {
				child = cur.path + "/" + e.Name()
			}
			// 符号链接一律不展开：它可能指向根外。
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				if cur.depth+1 <= MaxIndexDepth {
					queue = append(queue, item{path: child, depth: cur.depth + 1})
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			if len(listing) >= MaxWalkFiles {
				truncated = true
				break
			}
			listing = append(listing, child)
		}
	}
	sort.Strings(listing)
	return listing, nil
}

// groupByParent 预建「目录 → 该目录下文件」的索引。
// @ 补全实际匹配的是 basename，按目录分桶能把候选集从全库缩到一层。
func groupByParent(listing []string) map[string][]string {
	out := make(map[string][]string, 256)
	for _, p := range listing {
		dir := "."
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir = p[:i]
		}
		out[dir] = append(out[dir], p)
	}
	return out
}

// rankMatches 对 query 做子序列匹配并排序。
//
// 评分贴合 @ 补全的实际用法：
//   - basename 连续子串命中权重最高（输入 "readme" 想拿到 README.md）
//   - 整路径连续子串次之
//   - 越短的路径越优先（同分时根目录文件排在深层文件前面）
//
// 刻意不用第三方模糊库：这里的排序只需要稳定可解释，
// 多一个依赖就多一处要跟着升级的面。
//
// 性能上这条路径每敲一个字就跑一次，所以有两处刻意的安排：
//  1. 不把候选复制进临时切片。实测那一步就是绝大部分分配的来源——
//     2000 个文件的索引一次查询要 97 KB，其中九成是这份拷贝。
//     改为直接遍历各目录桶。
//  2. 大小写转换放在命中之后。basename 精确命中是常见情形，
//     那种情况下根本不需要为每个候选各做一次 ToLower。
func rankMatches(byParent map[string][]string, query string) []IndexMatch {
	needle := strings.ToLower(query)
	// query 含 '/' 时只在对应目录里找，否则扫全部桶。
	var dir string
	var scoped []string
	scopedQuery := false
	if i := strings.LastIndexByte(needle, '/'); i >= 0 {
		scopedQuery = true
		dir = needle[:i]
		if dir == "" {
			dir = "."
		}
		scoped = byParent[dir]
	}

	type scored struct {
		path  string
		score int
	}
	best := make([]scored, 0, 64)
	// visit 对单个候选打分并收进结果。
	//
	// lower 形式按需计算且只算一次。曾经每个候选固定做两次 ToLower，
	// 实测那正是查询路径的主要 CPU 开销——大多数候选在第一步就被
	// 大小写敏感的 Contains 筛掉，根本走不到需要 lower 的分支。
	visit := func(p string) {
		base := pathBase(p)
		var lower, baseLower string
		lowerOf := func() string {
			if lower == "" {
				lower = strings.ToLower(p)
			}
			return lower
		}
		baseLowerOf := func() string {
			if baseLower == "" {
				baseLower = strings.ToLower(base)
			}
			return baseLower
		}
		score := 0
		switch {
		case strings.Contains(base, needle):
			score = 3000 - len(base)
		case strings.Contains(p, needle):
			score = 2000 - len(p)
		case strings.Contains(baseLowerOf(), needle):
			score = 3000 - len(base)
		case strings.Contains(lowerOf(), needle):
			score = 2000 - len(p)
		case isSubsequence(baseLowerOf(), needle):
			score = 1000 - len(base)
		case isSubsequence(lowerOf(), needle):
			score = 500 - len(p)
		default:
			return
		}
		// 同名文件多处出现时，层级浅的更可能是想要的。
		score -= strings.Count(p, "/")
		best = append(best, scored{path: p, score: score})
	}
	if scopedQuery {
		// 目录不存在时 scoped 为 nil：应返回空结果，而不是把「不存在的前缀」
		// 退化成全库扫描（那会给出与用户输入无关的命中）。
		base := needle[strings.LastIndexByte(needle, '/')+1:]
		for _, p := range scoped {
			if strings.Contains(pathBase(p), base) || strings.Contains(strings.ToLower(pathBase(p)), base) {
				visit(p)
			}
		}
	} else {
		for _, files := range byParent {
			for _, p := range files {
				visit(p)
			}
		}
	}
	sort.Slice(best, func(i, j int) bool {
		if best[i].score != best[j].score {
			return best[i].score > best[j].score
		}
		return best[i].path < best[j].path
	})
	if len(best) > MaxIndexMatch {
		best = best[:MaxIndexMatch]
	}
	out := make([]IndexMatch, 0, len(best))
	for _, b := range best {
		out = append(out, IndexMatch{Path: b.path})
	}
	return out
}

// pathBase 取路径末段，等价于 filepath.Base 但不碰操作系统分隔符——
// 索引里一律是斜杠，跨平台行为才一致。
func pathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isSubsequence 判断 needle 是否为 text 的子序列。
func isSubsequence(text, needle string) bool {
	if needle == "" {
		return true
	}
	i := 0
	for j := 0; j < len(text) && i < len(needle); j++ {
		if text[j] == needle[i] {
			i++
		}
	}
	return i == len(needle)
}
