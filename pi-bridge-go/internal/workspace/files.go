package workspace

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// MaxReadBytes 是单次文件读取的体积上限（只读访问器）。
//
// 它是「一次读取最多产生多大的响应」这个事实的唯一来源：任何要把
// 文件内容整体搬运出去的中转（云端 HTTP 转发就是）都必须按它定预算，
// 否则调大上限之后，内容在中转处被静默截断或拒绝。
func (f *Files) MaxReadBytes() int64 { return f.limits.MaxReadByte }

// Files 在授权根内提供只读文件访问。
// 每个根各开一个 os.Root，路径解析与符号链接逃逸都由内核侧拦截，
// 不像纯字符串前缀比较那样可被 ../ 或链接绕过。
type Files struct {
	policy  *Policy
	limits  Limits
	gitPath string

	// mu 保护 roots（已打开的根目录句柄）、protected（写入拒绝清单）
	// 与 uploadBases（固定目标上传的批准基路径）。
	// 打开句柄是慢操作，因此临界区只覆盖查表与插入，不要在持锁期间遍历目录。
	mu           sync.Mutex
	roots        map[string]*os.Root
	scratchRoots map[string]*os.Root
	protected    []string
	uploadBases  []string

	// indexMu 保护 indexCache。它与 mu **从不同时持有**：索引构建会遍历
	// 大量文件，构建期间已经放开 indexMu（见 indexFor），因此不存在
	// 「先取哪把锁」的问题——两把锁各自独立。
	indexMu    sync.Mutex
	indexCache map[string]*indexEntry
}

// NewFiles 构造文件访问器。
func NewFiles(policy *Policy, limits Limits) (*Files, error) {
	if policy == nil || len(policy.roots) == 0 {
		return nil, protocol.E("invalid_params", "至少需要一个工作区根目录")
	}
	f := &Files{policy: policy, limits: limits, roots: map[string]*os.Root{}}
	// 可执行文件取自启动环境并固定为绝对路径，请求不能选择另一个 Git。
	if path, err := exec.LookPath("git"); err == nil && filepath.IsAbs(path) {
		f.gitPath, _ = filepath.EvalSymlinks(path)
	}
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
	for _, r := range f.scratchRoots {
		_ = r.Close()
	}
	f.scratchRoots = map[string]*os.Root{}
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

// rootFor 取工作区根句柄。
//
// 句柄是启动时打开的 fd，它绑定的是**当初那个 inode**：若根目录被改名
// 重建，路径校验会看到新目录，而写入仍落到旧 inode（review 时实测复现：
// 上传返回 200，文件却出现在 `根目录-old` 里）。因此这里比对一次 inode，
// 不一致就重开——「被校验的父目录」和「实际写入的父目录」必须是同一个。
func (f *Files) rootFor(root string) (*os.Root, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.roots[root]
	if !ok {
		return nil, protocol.E("forbidden", "工作区根未打开")
	}
	if !sameInode(r, root) {
		_ = r.Close()
		fresh, err := os.OpenRoot(root)
		if err != nil {
			delete(f.roots, root)
			return nil, protocol.E("not_found", "工作区根已不可用")
		}
		f.roots[root] = fresh
		return fresh, nil
	}
	return r, nil
}

// sameInode 判断根句柄与路径是否指向同一个目录。
// 句柄一侧用 Root.Stat(".")（不经过任何中间段），路径一侧用 os.Stat
// （根路径在 Policy 里已是真实路径，不存在链接歧义）。
func sameInode(r *os.Root, path string) bool {
	fi, err := r.Stat(".")
	if err != nil {
		return false
	}
	di, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(fi, di)
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
	dirFile, err := r.Open(rel)
	if err != nil {
		return nil, false, protocol.E("pi_error", "无法读取目录")
	}
	defer dirFile.Close()
	out := make([]Entry, 0, 64)
	truncated := false
	// 流式分批读取，边读边按上限截断。旧写法用 fs.ReadDir 一次性取回
	// 整个目录再排序，超大单目录会在限额检查之前就占满内存（B27）。
	for !truncated {
		items, rerr := dirFile.ReadDir(128)
		for _, e := range items {
			if len(out) >= f.limits.MaxEntries {
				truncated = true
				break
			}
			// 符号链接不展开，避免列出指向根外的内容。
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			out = append(out, Entry{
				Name: e.Name(), Path: filepath.Join(path, e.Name()), IsDir: e.IsDir(),
				Size: info.Size(), ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, false, protocol.E("pi_error", "无法读取目录")
		}
		if len(items) == 0 {
			break
		}
	}
	// 目录在前、各自按名称有序，与旧排序规则保持一致。
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, truncated, nil
}

// Read 读取文件内容，超出上限时只返回头部并标记 truncated。
// 只读取文本可安全展示的大小；二进制与超大文件一律拒绝。
// errTooLarge 表示读取过程中文件超过了限额。
var errTooLarge = errors.New("读取超过体积上限")

// readAtMost 最多读 limit 字节；多出一个字节就判定超限。
// 用 LimitReader 而不是 ReadFile：后者按 stat 的尺寸一次分配，
// 而 stat 与读之间文件可以增长，增长后的内容会被无上限地读进来（B51）。
func readAtMost(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// Read 以文本形式读取文件。上限作用于实际读取字节，而不只是 stat 结论（B51）。
func (f *Files) Read(path string) (string, bool, int64, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return "", false, 0, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return "", false, 0, err
	}
	file, err := r.Open(rel)
	if err != nil {
		return "", false, 0, protocol.E("not_found", "路径不存在")
	}
	defer file.Close()
	// 在打开后的句柄上取元数据：与接下来读的是同一个 inode。
	info, err := file.Stat()
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
	b, err := readAtMost(file, f.limits.MaxReadByte)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return "", false, size, protocol.E("limit_exceeded", "文件超过可读取体积上限")
		}
		return "", false, size, protocol.E("pi_error", "读取失败")
	}
	// 二进制不当文本读。此前 PNG 会被 string(b) 转成乱码返回，
	// 前端照着渲染出一屏替换字符；现在明确告知调用方该走别的路径。
	if kind := DetectBinary(b); kind != "" {
		return "", false, size, protocol.E("unsupported", kind)
	}
	return string(b), false, size, nil
}

// Image 返回图片字节与 MIME，供文件查看器内联显示。
// 与 Read 分开是因为图片不该经过 UTF-8 转换——那会破坏像素数据。
func (f *Files) Image(path string) ([]byte, string, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return nil, "", err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return nil, "", err
	}
	file, err := r.Open(rel)
	if err != nil {
		return nil, "", protocol.E("not_found", "路径不存在")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", protocol.E("not_found", "路径不存在")
	}
	if !info.Mode().IsRegular() {
		return nil, "", protocol.E("invalid_params", "只能读取普通文件")
	}
	if info.Size() > f.limits.MaxReadByte {
		return nil, "", protocol.E("limit_exceeded", "图片超过体积上限")
	}
	b, err := readAtMost(file, f.limits.MaxReadByte)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return nil, "", protocol.E("limit_exceeded", "图片超过体积上限")
		}
		return nil, "", protocol.E("pi_error", "读取失败")
	}
	mime := ImageMime(b)
	if mime == "" {
		return nil, "", protocol.E("unsupported", "不是受支持的图片格式")
	}
	return b, mime, nil
}

// Roots 返回全部授权根。
func (f *Files) Roots() []string {
	out := make([]string, len(f.policy.roots))
	copy(out, f.policy.roots)
	return out
}

// Protect 声明写入时一律拒绝的路径前缀（真实路径）。
//
// 为什么需要它：生产的工作区根是 /opt/helloworld，而桥自己的二进制、
// 启动脚本与 UI 发布目录都在这棵树里。只靠「必须在工作区根内」这一条，
// 网页就能把二进制换掉——那是「网页能不能写文件」变成「网页能不能接管
// 服务」的差别。拒绝清单由启动方按运行时事实给出（可执行文件所在目录、
// UI 目录、状态目录、agent 目录），而不是在这里写死某个部署路径。
//
// 传入的路径先解析符号链接；解析失败的直接按原样保留（宁严勿松）。
func (f *Files) Protect(paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		f.protected = append(f.protected, filepath.Clean(abs))
	}
}

// uploadSubdir 是固定目标上传的固定中间层：<base>/piwebui/<会话id>/。
//
// 不让文件直接散落在 <base>/ 下：base 可能是 /tmp 或 /scratch 这类
// 与别人共用的目录，加一层自己的名字既方便整体清理，也避免与别人的
// 同名文件相撞。
const uploadSubdir = "piwebui"

// SetUploadBases 声明允许作为「固定上传目标」的基路径白名单。
//
// 为什么需要白名单：固定目标模式会把文件写到工作区根之外（例如
// /tmp/piwebui/<会话id>/），那是桥原本不具备的能力。如果前端能传任意
// 绝对路径进来，「网页能上传文件」立刻变成「网页能写全盘」。因此哪些
// 基路径可用必须由启动方（命令行）决定，网页只能在白名单里选。
//
// 只保留真实存在的目录：不存在的基路径不预先创建——上传本身会建
// <base>/piwebui/<会话id>/，但基路径自己应当由部署方保证。
func (f *Files) SetUploadBases(paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Clean(p))
		if err != nil {
			continue
		}
		if st, err := os.Stat(real); err != nil || !st.IsDir() {
			continue
		}
		if !slices.Contains(f.uploadBases, real) {
			f.uploadBases = append(f.uploadBases, real)
		}
	}
}

// UploadBases 返回已批准的上传基路径（真实路径）。
func (f *Files) UploadBases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uploadBases...)
}

// ShadowedUploadBases 返回那些「已批准但落在受保护目录里」的基路径。
//
// 为什么要有这个查询：受保护判定优先于上传白名单（否则把受保护目录声明
// 成基路径就能绕开保护）。但一个配置了 --upload-base 却永远写不进去的
// 部署会很难查——上传只会回一句「该目录不允许写入」，看不出是白名单与
// 保护清单撞了。启动方应当在启动时把这份清单打出来。
func (f *Files) ShadowedUploadBases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, base := range f.uploadBases {
		for _, p := range f.protected {
			if base == p || strings.HasPrefix(base, p+string(filepath.Separator)) {
				out = append(out, base)
				break
			}
		}
	}
	return out
}

// scratchDir 计算固定目标：返回**已验证的基路径真实路径**与其下的相对路径
// piwebui/<会话id>。
//
// 为什么拆开返回而不直接拼绝对路径：基路径（/tmp、/scratch）可能是世界
// 可写的共享目录，中间层 piwebui 完全可能被别的进程预先放成指向根外的
// 符号链接。只对 base 做 EvalSymlinks 挡不住这一点——必须把后续的建目录
// 与写文件都放进「以 base 为根的 os.Root」里，让内核在解析每一段时都把
// 逃逸挡回去（见 UploadScratchNamed）。
func (f *Files) scratchDir(base, sessionID string) (baseReal, rel string, err error) {
	if !safePathSegment(sessionID) {
		return "", "", protocol.E("invalid_params", "会话 ID 不合法")
	}
	if base == "" {
		return "", "", protocol.E("invalid_params", "缺少上传基路径")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(base))
	if err != nil {
		return "", "", protocol.E("not_found", "上传基路径不存在")
	}
	f.mu.Lock()
	approved := slices.Contains(f.uploadBases, real)
	f.mu.Unlock()
	if !approved {
		return "", "", protocol.E("forbidden", "上传基路径未获批准")
	}
	return real, filepath.Join(uploadSubdir, sessionID), nil
}

// UploadScratch 按固定目标规则上传：<base>/piwebui/<会话id>/<文件名>。
// 文件名为空时用 "upload"。
func (f *Files) UploadScratch(src io.Reader, base, sessionID string) (string, error) {
	return f.UploadScratchNamed(src, base, sessionID, "")
}

// UploadScratchNamed 是 UploadScratch 的显式命名版本。
//
// 与 Upload 的差别：目录不存在时会创建（<base>/piwebui/<会话id>/ 是
// 桥的领地，不是用户手写的目录树），但文件本身仍然 O_EXCL 不覆盖，
// 受保护目录判定也照旧生效。
//
// 建目录与写文件都在「以 base 为根的 os.Root」内进行：base 可能是共享
// 目录，中间层 piwebui 若是预先放置的符号链接，绝对路径写法会一路跟到
// 根外（review 时实测复现过：/tmp/piwebui → 别处，文件落到了根外）。
// os.Root 让内核在每一段解析上都拒绝逃逸，这是它与字符串前缀比对的
// 根本差别。
func (f *Files) UploadScratchNamed(src io.Reader, base, sessionID, name string) (string, error) {
	baseReal, rel, err := f.scratchDir(base, sessionID)
	if err != nil {
		return "", err
	}
	fileName := strings.TrimSpace(name)
	if fileName == "" {
		fileName = "upload"
	}
	clean, ok := sanitizeBaseName(fileName)
	if !ok {
		return "", protocol.E("invalid_params", "文件名不合法")
	}
	// 受保护判定用拼回的绝对路径（真实基路径 + 相对路径）。
	abs := filepath.Join(baseReal, rel)
	if f.isProtected(abs) {
		return "", protocol.E("forbidden", "该目录不允许写入")
	}
	// 以 base 为根打开 os.Root：之后所有操作都相对它，中间层符号链接
	// 无法再逃逸。0700 是因为 scratch 里是上传内容与会话产物，不给
	// 同机其它用户看。
	r, err := f.scratchRoot(baseReal)
	if err != nil {
		return "", err
	}
	if err := r.MkdirAll(rel, 0o700); err != nil {
		return "", protocol.E("forbidden", "无法创建上传目录")
	}
	// 建完目录必须**重新**解析真实路径再判一次受保护。
	//
	// 上面的字符串比对只能看见「按名字拼出来的路径」，而 os.Root 允许
	// 跟随指向根内的**相对**符号链接：base 里若被预先放了
	// `piwebui -> protected`，拼接结果仍叫 base/piwebui/...，实际却落在
	// 受保护目录里（review 时实测复现）。目录已由 MkdirAll 建好（或本就
	// 存在），此刻 EvalSymlinks 能看到真实落点；仍以 base 为根写文件，
	// 因此「解析到的落点」与「真正写入的位置」是同一个。
	real, rerr := filepath.EvalSymlinks(abs)
	if rerr != nil {
		return "", protocol.E("forbidden", "无法解析上传目录")
	}
	if f.isProtected(real) {
		return "", protocol.E("forbidden", "该目录不允许写入")
	}
	target := filepath.Join(real, clean)
	return target, f.writeScratchFile(r, filepath.Join(rel, clean), src)
}

// isProtected 判断某个**已解析的真实路径**是否落在受保护清单内。
// 清单里的路径由 Protect 规范化，调用方必须传入真实路径，否则字符串
// 比对会漏（见 UploadScratchNamed 里「建完目录再解析一次」的理由）。
func (f *Files) isProtected(real string) bool {
	f.mu.Lock()
	protected := append([]string(nil), f.protected...)
	f.mu.Unlock()
	for _, p := range protected {
		if real == p || strings.HasPrefix(real, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// scratchRoot 按基路径打开并缓存 os.Root。
//
// 与 roots（工作区根句柄）分开缓存：两者的生命周期与语义都不同，
// 混在一起会让「哪个根管哪条路径」变模糊。
func (f *Files) scratchRoot(base string) (*os.Root, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.scratchRoots[base]; ok {
		if sameInode(r, base) {
			return r, nil
		}
		// 基路径被删除重建：旧 fd 指向已消失的 inode，继续用会把文件
		// 写进「看不见的旧目录」，而响应里的路径是新的（实测复现）。
		_ = r.Close()
		delete(f.scratchRoots, base)
	}
	r, err := os.OpenRoot(base)
	if err != nil {
		return nil, protocol.E("not_found", "上传基路径不存在")
	}
	if f.scratchRoots == nil {
		f.scratchRoots = map[string]*os.Root{}
	}
	f.scratchRoots[base] = r
	return r, nil
}

// writeScratchFile 在 scratch 根内独占创建文件。
func (f *Files) writeScratchFile(r *os.Root, rel string, src io.Reader) error {
	_, err := f.writeExclusive(func(perm os.FileMode) (*os.File, error) {
		return r.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	}, func() { _ = r.Remove(rel) }, src, 0o644)
	return err
}

// safePathSegment 判断一个字符串能否安全地作为单段路径成分。
//
// 只允许「字母、数字、连字符、下划线、点」且不能是 . 或 ..：会话 id
// 来自客户端，拼进路径前必须过这一关。不用 sessions.ValidID 是因为
// sessions 包依赖本包，反向引用会形成循环。
func safePathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// sanitizeBaseName 取基名并拒绝可疑形态。与 transport 层的清洗同义，
// 这里再一遍是因为 scratch 路径由多个来源拼接（会话 id、文件名）。
func sanitizeBaseName(name string) (string, bool) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, 0) {
		return "", false
	}
	if name != filepath.Base(name) {
		return "", false
	}
	return name, true
}

// resolveTarget 把「准备写入的目标路径」映射到授权根内。
//
// 与 resolve 的差别：目标**还不存在**，所以不能对整条路径做 EvalSymlinks。
// 做法是解析父目录的真实路径，再把文件基名原样接上——这样最后一段的
// 符号链接不会被跟随（配合 O_EXCL，指向链接的写入直接失败），而父目录
// 里的 .. 与链接逃逸仍然被真实路径比对挡住。
func (f *Files) resolveTarget(path string) (root, rel string, err error) {
	if path == "" {
		return "", "", protocol.E("invalid_params", "path 不能为空")
	}
	if !filepath.IsAbs(path) {
		return "", "", protocol.E("invalid_params", "path 必须是绝对路径")
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	// 基名必须是普通文件名：上游已经把 filename 清洗过，这里是最后一道。
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return "", "", protocol.E("invalid_params", "文件名不合法")
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", "", protocol.E("not_found", "目标目录不存在")
	}
	if st, serr := os.Stat(parentReal); serr != nil || !st.IsDir() {
		return "", "", protocol.E("not_found", "目标目录不存在")
	}
	best := ""
	for _, r := range f.policy.roots {
		relDir, rerr := filepath.Rel(r, parentReal)
		if rerr != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
			continue
		}
		if best == "" || len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return "", "", protocol.E("forbidden", "路径不在已配置的工作区根内")
	}
	full := filepath.Join(parentReal, base)
	f.mu.Lock()
	protected := append([]string(nil), f.protected...)
	f.mu.Unlock()
	for _, p := range protected {
		if full == p || strings.HasPrefix(full, p+string(filepath.Separator)) {
			return "", "", protocol.E("forbidden", "该目录不允许写入")
		}
	}
	relDir, err := filepath.Rel(best, parentReal)
	if err != nil {
		return "", "", protocol.E("forbidden", "无法解析相对路径")
	}
	return best, filepath.ToSlash(filepath.Join(relDir, base)), nil
}

// errUploadExists 表示目标已存在。上传**永不覆盖**：覆盖是网页能造成
// 的最容易误触的破坏（同名文件撞上用户源码），而「想改内容」有编辑类
// 工具，不该由上传顺手完成。
var errUploadExists = protocol.E("conflict", "目标文件已存在，上传不覆盖")

// Upload 把一个流写入工作区根内的新文件，返回写入字节数。
//
// 为什么会整体读进内存再写：上限是 MaxReadByte（默认 4 MiB），与读取
// 上限同源；一次性读入让「超限」发生在任何字节落盘之前，因此失败不会
// 留下半截文件——半截文件比明确失败更糟，它会被后续工具当成完整输入。
//
// 落盘用 os.Root 的 O_CREATE|O_EXCL：内核侧保证不跟随最后一段符号链接、
// 且目标已存在时直接失败，不依赖 Go 侧先查后写（那之间有竞态）。
func (f *Files) Upload(src io.Reader, path string, perm os.FileMode) (int64, error) {
	root, rel, err := f.resolveTarget(path)
	if err != nil {
		return 0, err
	}
	r, err := f.rootFor(root)
	if err != nil {
		return 0, err
	}
	return f.writeExclusive(func(perm os.FileMode) (*os.File, error) {
		return r.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	}, func() { _ = r.Remove(rel) }, src, perm)
}

// writeExclusive 是所有上传落盘的共同实现：先整体读入（保证超限不留
// 半截文件），再以 O_EXCL 独占创建（保证不覆盖、不跟随最后一段链接）。
// open 与 cleanup 由调用方给出，因为工作区根内走 os.Root、固定目标走
// 各自基路径的 os.Root，两者的句柄来源不同。
func (f *Files) writeExclusive(open func(os.FileMode) (*os.File, error), cleanup func(), src io.Reader, perm os.FileMode) (int64, error) {
	// 为什么会整体读进内存再写：上限是 MaxReadByte（默认 4 MiB），与读取
	// 上限同源；一次性读入让「超限」发生在任何字节落盘之前，因此失败不会
	// 留下半截文件——半截文件比明确失败更糟，它会被后续工具当成完整输入。
	data, err := readAtMost(src, f.limits.MaxReadByte)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return 0, protocol.E("limit_exceeded", "文件超过上传体积上限")
		}
		return 0, protocol.E("invalid_request", "读取上传内容失败")
	}
	if len(data) == 0 {
		return 0, protocol.E("invalid_params", "文件内容为空")
	}
	if perm == 0 {
		perm = 0o644
	}
	file, err := open(perm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, errUploadExists
		}
		return 0, protocol.E("forbidden", "无法创建目标文件")
	}
	n, werr := file.Write(data)
	cerr := file.Close()
	if werr != nil || cerr != nil {
		// 写一半失败：删掉半截文件再报错。
		cleanup()
		return 0, protocol.E("pi_error", "写入文件失败")
	}
	return int64(n), nil
}
