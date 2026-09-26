package workspace

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
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
// truncated 只在无 query 的分支有意义：列表被上限截断时，
// 客户端应提示用户改用更精确的查询，而不是让人以为项目就这么大。
type IndexResult struct {
	Files     []string     `json:"files"`
	Matches   []IndexMatch `json:"matches,omitempty"`
	Truncated bool         `json:"truncated"`
}

type indexEntry struct {
	listing  []string
	byParent map[string][]string
	expireAt time.Time
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
		truncated := false
		if len(files) > MaxIndexFiles {
			files = files[:MaxIndexFiles]
			truncated = true
		}
		return IndexResult{Files: files, Truncated: truncated}, nil
	}
	return IndexResult{Matches: rankMatches(entry.byParent, query)}, nil
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
	listing, err := f.listIndex(ctx, root, rel, abs)
	if err != nil {
		return nil, err
	}
	entry := &indexEntry{listing: listing, byParent: groupByParent(listing), expireAt: now.Add(IndexCacheTTL)}

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

// listIndex 优先用 git（尊重 .gitignore），失败或非仓库则退回复制目录 walk。
// 两条路都受硬上限约束，且都不跟随符号链接。
func (f *Files) listIndex(ctx context.Context, root, rel, abs string) ([]string, error) {
	if listing, ok := f.listWithGit(ctx, abs); ok {
		return listing, nil
	}
	return f.listWithWalk(root, rel)
}

// listWithGit 用 git ls-files 取已跟踪与未忽略文件。
// 返回 ok=false 表示不是 git 仓库或 git 不可用，调用方应走 walk 兜底。
func (f *Files) listWithGit(ctx context.Context, abs string) ([]string, bool) {
	// git 不在授权根内也能用：它只读 cwd，不做任意命令执行。
	cmdCtx, cancel := context.WithTimeout(ctx, GitListTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", "-C", abs, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Dir = abs
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	listing := make([]string, 0, 1024)
	truncated := false
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		if len(listing) >= MaxWalkFiles {
			truncated = true
			break
		}
		listing = append(listing, filepath.ToSlash(name))
	}
	// 截断时保留最短的一批：浅路径通常是更可能被 @ 引用的目标。
	if truncated {
		sort.Slice(listing, func(i, j int) bool {
			return len(listing[i]) < len(listing[j])
		})
		listing = listing[:MaxWalkFiles]
	}
	return listing, true
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
func rankMatches(byParent map[string][]string, query string) []IndexMatch {
	needle := strings.ToLower(query)
	// 候选集：query 含 '/' 时只在对应目录里找，否则扫全部桶。
	var candidates []string
	if i := strings.LastIndexByte(needle, '/'); i >= 0 {
		dir := needle[:i]
		base := needle[i+1:]
		if dir == "" {
			dir = "."
		}
		for _, p := range byParent[dir] {
			if strings.Contains(strings.ToLower(pathBase(p)), base) {
				candidates = append(candidates, p)
			}
		}
	} else {
		for _, files := range byParent {
			candidates = append(candidates, files...)
		}
	}

	type scored struct {
		path  string
		score int
	}
	best := make([]scored, 0, 64)
	for _, p := range candidates {
		lower := strings.ToLower(p)
		base := strings.ToLower(pathBase(p))
		score := 0
		switch {
		case strings.Contains(base, needle):
			score = 3000 - len(base)
		case strings.Contains(lower, needle):
			score = 2000 - len(p)
		case isSubsequence(base, needle):
			score = 1000 - len(base)
		case isSubsequence(lower, needle):
			score = 500 - len(p)
		default:
			continue
		}
		// 同名文件多处出现时，层级浅的更可能是想要的。
		score -= strings.Count(p, "/")
		best = append(best, scored{path: p, score: score})
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
