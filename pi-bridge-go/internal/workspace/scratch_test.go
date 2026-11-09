package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 固定目标上传（scratch 模式）：文件不落在工作区根内，而是落在
// 一个显式批准的「上传基础路径」下的 piwebui/<会话id>/。
//
// 为什么需要这个模式：用户可能希望上传的东西集中收集、不污染工作区。
// 但工作区根外写文件是危险能力，因此基础路径必须是启动方声明的白名单，
// 网页只能在白名单内选——不能让前端传一个任意绝对路径进来。

func newScratchFiles(t *testing.T, bases ...string) (*Files, []string) {
	t.Helper()
	root := t.TempDir()
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	dirs := make([]string, 0, len(bases))
	for _, b := range bases {
		d := t.TempDir()
		if b != "" {
			d = b
		}
		dirs = append(dirs, d)
	}
	f.SetUploadBases(dirs...)
	return f, dirs
}

const testSession = "01a1ba81-1c77-72b4-bf0c-31c986eeab7b"

func Test固定目标上传建目录并落盘(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	target, err := f.UploadScratch(strings.NewReader("scratch payload"), dirs[0], testSession)
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	want := dirs[0] + "/piwebui/" + testSession + "/upload"
	if !strings.HasPrefix(target, dirs[0]+"/piwebui/"+testSession+"/") {
		t.Fatalf("落点不在预期目录：%s（期望前缀 %s）", target, want)
	}
}

func Test固定目标上传按给定文件名(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	name := "报告 2026.txt"
	target, err := f.UploadScratchNamed(strings.NewReader("中文名"), dirs[0], testSession, name)
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	if !strings.HasSuffix(target, "/"+name) {
		t.Fatalf("文件名未保留：%s", target)
	}
}

// 基础路径必须在白名单里。前端传什么就写什么，等于网页可写全盘。
func Test固定目标上传拒绝未批准的基础路径(t *testing.T) {
	f, _ := newScratchFiles(t, "")
	if _, err := f.UploadScratchNamed(strings.NewReader("x"), "/etc", testSession, "pwn.txt"); err == nil {
		t.Fatal("未批准的基路径应当被拒绝")
	}
}

// 会话 id 会拼进路径，因此必须校验形状：`../..` 之类不能逃出 piwebui。
func Test固定目标上传拒绝非法会话id(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	for _, bad := range []string{"", "..", "../..", "a/b", "a\\b", "."} {
		if _, err := f.UploadScratchNamed(strings.NewReader("x"), dirs[0], bad, "f.txt"); err == nil {
			t.Fatalf("非法会话 id 应被拒绝：%q", bad)
		}
	}
}

// 同一会话多次上传：目录已存在要复用，不能报错。
func Test固定目标上传复用已存在目录(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	if _, err := f.UploadScratchNamed(strings.NewReader("1"), dirs[0], testSession, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.UploadScratchNamed(strings.NewReader("2"), dirs[0], testSession, "b.txt"); err != nil {
		t.Fatalf("同会话第二次上传不该失败：%v", err)
	}
}

// 文件名冲突仍然拒绝：scratch 不是「可以覆盖」的豁免区。
func Test固定目标上传仍不覆盖(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	if _, err := f.UploadScratchNamed(strings.NewReader("原件"), dirs[0], testSession, "dup.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.UploadScratchNamed(strings.NewReader("新的"), dirs[0], testSession, "dup.txt"); err == nil {
		t.Fatal("同名文件应被拒绝")
	}
}

// 受保护目录优先于上传基础路径：即使把某个受保护目录声明成基础路径，
// 也不允许写。这防止部署时误配把桥二进制目录变成可写。
func Test固定目标上传受保护目录优先(t *testing.T) {
	f, _ := newScratchFiles(t, "")
	protected := t.TempDir()
	f.Protect(protected)
	f.SetUploadBases(protected)
	if _, err := f.UploadScratchNamed(strings.NewReader("x"), protected, testSession, "f.txt"); err == nil {
		t.Fatal("受保护目录即使被声明为基础路径也不该可写")
	}
}

func Test上传基础路径列表可读(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	got := f.UploadBases()
	if len(got) != len(dirs) {
		t.Fatalf("基路径数量 %d，期望 %d：%v", len(got), len(dirs), got)
	}
}

// 受保护目录会把上传基路径「罩住」：这种情况必须能被查询出来，
// 否则部署时只会看到一句「该目录不允许写入」，查不到是两套清单撞了。
func Test被保护的基路径可查(t *testing.T) {
	f, _ := newScratchFiles(t, "")
	ok := t.TempDir()
	protected := t.TempDir()
	f.Protect(protected)
	f.SetUploadBases(ok, protected)

	shadowed := f.ShadowedUploadBases()
	if len(shadowed) != 1 {
		t.Fatalf("应当只报告 1 个被罩住的基路径，实际 %v", shadowed)
	}
	if shadowed[0] != protected {
		t.Fatalf("报告的是 %s，期望 %s", shadowed[0], protected)
	}
}

// 中间层符号链接逃逸（review 时发现的真实漏洞）：
// base 可能是世界可写的共享目录（/tmp、/scratch），piwebui 这层完全可能
// 被别的进程预先放成指向根外的符号链接。只对 base 做 EvalSymlinks 挡不住
// 这一点——建目录与写文件必须都在「以 base 为根的 os.Root」里进行。
func Test固定目标拒绝中间层符号链接逃逸(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	base := dirs[0]
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "piwebui")); err != nil {
		t.Skipf("无法建符号链接：%v", err)
	}
	_, err := f.UploadScratchNamed(strings.NewReader("x"), base, testSession, "evil.txt")
	// 断言落点而不是只断言「报错了」：任何错误都通过的话，这条测试在
	// 「因为别的原因失败」时也会变绿，等于没测到逃逸这一层。
	if err == nil {
		t.Fatal("中间层符号链接逃逸应当被拒绝")
	}
	matches, _ := filepath.Glob(filepath.Join(outside, "*", "evil.txt"))
	if len(matches) > 0 {
		t.Fatalf("符号链接逃逸：文件写到了根外 %v", matches)
	}
	if _, serr := os.Stat(filepath.Join(outside, "evil.txt")); serr == nil {
		t.Fatal("符号链接逃逸：文件写到了根外")
	}
}

// 会话 id 那层若是符号链接，同样不能跟出去。
func Test固定目标拒绝会话层符号链接逃逸(t *testing.T) {
	f, dirs := newScratchFiles(t, "")
	base := dirs[0]
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "piwebui"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "piwebui", testSession)); err != nil {
		t.Skipf("无法建符号链接：%v", err)
	}
	if _, err := f.UploadScratchNamed(strings.NewReader("x"), base, testSession, "evil.txt"); err == nil {
		t.Fatal("会话层符号链接逃逸应当被拒绝")
	}
	if _, serr := os.Stat(filepath.Join(outside, "evil.txt")); serr == nil {
		t.Fatal("会话层符号链接逃逸：文件写到了根外")
	}
}

// 相对符号链接绕过受保护判定（独立复审实测复现）：
// os.Root 允许跟随「指向根内」的相对链接，而受保护判定原先只看
// 拼出来的绝对路径字符串。base 里预置 `piwebui -> protected` 时，
// 拼接结果仍叫 base/piwebui/...，实际落点却在受保护目录里。
func Test固定目标不得经相对符号链接写进受保护目录(t *testing.T) {
	base := t.TempDir()
	protected := filepath.Join(base, "protected")
	if err := os.MkdirAll(protected, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(mustTestPolicy(t, t.TempDir()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	f.Protect(protected)
	f.SetUploadBases(base)
	if err := os.Symlink("protected", filepath.Join(base, "piwebui")); err != nil {
		t.Skipf("无法建符号链接：%v", err)
	}
	if _, uerr := f.UploadScratchNamed(strings.NewReader("evil"), base, testSession, "evil.txt"); uerr == nil {
		t.Fatal("经相对符号链接写进受保护目录应当被拒绝")
	}
	if _, serr := os.Stat(filepath.Join(protected, testSession, "evil.txt")); serr == nil {
		t.Fatalf("文件写进了受保护目录 %s", protected)
	}
}

// 基路径被删除重建后，缓存的 os.Root 指向已消失的 inode：
// 上传会返回 200 并把文件写进「看不见的旧目录」，响应路径却是新的。
func Test固定目标在基路径重建后不得写进旧目录(t *testing.T) {
	base := filepath.Join(t.TempDir(), "up")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(mustTestPolicy(t, t.TempDir()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	f.SetUploadBases(base)
	// 先上传一次，让 scratchRoots 缓存住句柄。
	if _, err := f.UploadScratchNamed(strings.NewReader("1"), base, testSession, "a.txt"); err != nil {
		t.Fatal(err)
	}
	old := base + "-old"
	if err := os.Rename(base, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.UploadScratchNamed(strings.NewReader("2"), base, testSession, "b.txt"); err != nil {
		t.Logf("重建后上传返回错误（可接受）：%v", err)
	}
	if _, serr := os.Stat(filepath.Join(old, "piwebui", testSession, "b.txt")); serr == nil {
		t.Fatalf("写进了被删掉的旧基路径（stale inode）：%s", old)
	}
}
