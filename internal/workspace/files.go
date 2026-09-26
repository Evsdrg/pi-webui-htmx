package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"pi-bridge-go/internal/protocol"
)

// Entry 是文件或目录的元数据。
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// Limits 约束文件访问的体积与条目数。
type Limits struct {
	MaxEntries  int
	MaxReadByte int64
	MaxDepth    int
}

func DefaultLimits() Limits { return Limits{MaxEntries: 500, MaxReadByte: 4 << 20, MaxDepth: 8} }

// Files 在授权根内提供只读文件访问。
// 每个根各开一个 os.Root，路径解析与符号链接逃逸都由内核侧拦截，
// 不像纯字符串前缀比较那样可被 ../ 或链接绕过。
type Files struct {
	policy *Policy
	limits Limits

	mu    sync.Mutex
	roots map[string]*os.Root

	indexMu    sync.Mutex
	indexCache map[string]*indexEntry
}

// NewFiles 构造文件访问器。
func NewFiles(policy *Policy, limits Limits) (*Files, error) {
	if policy == nil || len(policy.roots) == 0 {
		return nil, protocol.E("invalid_params", "至少需要一个工作区根目录")
	}
	f := &Files{policy: policy, limits: limits, roots: map[string]*os.Root{}}
	for _, root := range policy.roots {
		r, err := os.OpenRoot(root)
		if err != nil {
			f.Close()
			return nil, err
		}
		f.roots[root] = r
	}
	return f, nil
}

func (f *Files) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.roots {
		_ = r.Close()
	}
	f.roots = map[string]*os.Root{}
}

// resolve 把绝对路径映射到某个授权根，并返回根内相对路径。
func (f *Files) resolve(path string) (string, string, error) {
	if path == "" {
		return "", "", protocol.E("invalid_params", "path 不能为空")
	}
	if !filepath.IsAbs(path) {
		return "", "", protocol.E("invalid_params", "path 必须是绝对路径")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", protocol.E("not_found", "路径不存在")
	}
	best := ""
	for _, root := range f.policy.roots {
		rel, err := filepath.Rel(root, real)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if best == "" || len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return "", "", protocol.E("forbidden", "路径不在已配置的工作区根内")
	}
	rel, err := filepath.Rel(best, real)
	if err != nil {
		return "", "", protocol.E("forbidden", "无法解析相对路径")
	}
	return best, filepath.ToSlash(rel), nil
}

func (f *Files) rootFor(root string) (*os.Root, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.roots[root]
	if !ok {
		return nil, protocol.E("forbidden", "工作区根未打开")
	}
	return r, nil
}

// Stat 返回单个文件或目录的元数据。
func (f *Files) Stat(path string) (Entry, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return Entry{}, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return Entry{}, err
	}
	info, err := r.Stat(rel)
	if err != nil {
		return Entry{}, protocol.E("not_found", "路径不存在")
	}
	name := filepath.Base(path)
	if rel == "." {
		name = filepath.Base(root)
	}
	return Entry{Name: name, Path: path, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z")}, nil
}

// List 列出目录内容，按目录优先、名称排序，条目数受上限约束。
func (f *Files) List(path string) ([]Entry, bool, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return nil, false, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return nil, false, err
	}
	info, err := r.Stat(rel)
	if err != nil {
		return nil, false, protocol.E("not_found", "路径不存在")
	}
	if !info.IsDir() {
		return nil, false, protocol.E("invalid_params", "目标不是目录")
	}
	entries, err := fs.ReadDir(r.FS(), rel)
	if err != nil {
		return nil, false, protocol.E("pi_error", "无法读取目录")
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	out := make([]Entry, 0, 64)
	truncated := false
	for i, e := range entries {
		if i >= f.limits.MaxEntries {
			truncated = true
			break
		}
		// 符号链接不展开，避免列出指向根外的内容。
		child := filepath.Join(path, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name: e.Name(), Path: child, IsDir: e.IsDir(),
			Size: info.Size(), ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, truncated, nil
}

// Read 读取文件内容，超出上限时只返回头部并标记 truncated。
// 只读取文本可安全展示的大小；二进制与超大文件一律拒绝。
func (f *Files) Read(path string) (string, bool, int64, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return "", false, 0, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return "", false, 0, err
	}
	info, err := r.Stat(rel)
	if err != nil {
		return "", false, 0, protocol.E("not_found", "路径不存在")
	}
	if !info.Mode().IsRegular() {
		return "", false, 0, protocol.E("invalid_params", "只能读取普通文件")
	}
	size := info.Size()
	if size > f.limits.MaxReadByte {
		return "", false, size, protocol.E("limit_exceeded", "文件超过可读取体积上限")
	}
	b, err := r.ReadFile(rel)
	if err != nil {
		return "", false, size, protocol.E("pi_error", "读取失败")
	}
	return string(b), false, size, nil
}

// Roots 返回全部授权根。
func (f *Files) Roots() []string {
	out := make([]string, len(f.policy.roots))
	copy(out, f.policy.roots)
	return out
}
