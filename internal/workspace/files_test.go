package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newFiles(t *testing.T, roots ...string) *Files {
	t.Helper()
	p, err := New(roots)
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(p, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

func TestList与Read正常路径(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	f := newFiles(t, root)
	entries, truncated, err := f.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(entries) != 2 {
		t.Fatalf("目录列表异常: %+v", entries)
	}
	// 目录优先，其次按名称。
	if !entries[0].IsDir || entries[0].Name != "sub" {
		t.Fatalf("排序异常: %+v", entries)
	}
	text, trunc, size, err := f.Read(filepath.Join(root, "a.txt"))
	if err != nil || trunc || text != "hello" || size != 5 {
		t.Fatalf("读取异常: %q %v %d %v", text, trunc, size, err)
	}
	st, err := f.Stat(filepath.Join(root, "a.txt"))
	if err != nil || st.IsDir {
		t.Fatalf(" stat 异常: %+v %v", st, err)
	}
}

func TestRead拒绝越界与目录(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "secret.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	f := newFiles(t, root)
	if _, _, _, err := f.Read(filepath.Join(other, "secret.txt")); err == nil {
		t.Fatal("越界读取必须被拒绝")
	}
	if _, _, _, err := f.Read(filepath.Join(root, "..", "..", "etc", "passwd")); err == nil {
		t.Fatal("路径穿越必须被拒绝")
	}
	if _, _, _, err := f.Read(root); err == nil {
		t.Fatal("读取目录必须被拒绝")
	}
	if _, _, err := f.List(filepath.Join(other)); err == nil {
		t.Fatal("越界列目录必须被拒绝")
	}
	if _, _, _, err := f.Read(filepath.Join(root, "missing.txt")); err == nil {
		t.Fatal("不存在文件必须被拒绝")
	}
}

func Test文件符号链接逃逸被拒绝(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "target.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(other, "target.txt"), link); err != nil {
		t.Skip("当前环境不支持符号链接")
	}
	f := newFiles(t, root)
	if _, _, _, err := f.Read(link); err == nil {
		t.Fatal("指向根外的符号链接必须被拒绝")
	}
	entries, _, err := f.List(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "link.txt" && !e.IsDir {
			// 符号链接可以列出，但不得被读取。
			if _, _, _, err := f.Read(e.Path); err == nil {
				t.Fatal("列出的符号链接仍不得读取")
			}
		}
	}
}

func Test超大文件被拒绝(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.bin")
	if err := os.WriteFile(big, make([]byte, 5<<20), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxReadByte = 1 << 20
	f, err := NewFiles(p, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, _, err := f.Read(big); err == nil {
		t.Fatal("超过体积上限必须被拒绝")
	}
}

func Test条目数上限(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		name := filepath.Join(root, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxEntries = 5
	f, err := NewFiles(p, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, truncated, err := f.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 || !truncated {
		t.Fatalf("条目上限未生效: %d %v", len(entries), truncated)
	}
}

func TestGit状态与diff(t *testing.T) {
	root := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f := newFiles(t, root)
	ctx := context.Background()
	status, err := f.GitStatus(ctx, root)
	if err != nil {
		t.Fatalf("git 状态失败: %v", err)
	}
	if status.Clean {
		t.Fatalf("有未跟踪文件时应判定为不干净: %+v", status)
	}
	files := status.Files
	if len(files) != 1 || files[0].Path != "new.txt" {
		t.Fatalf("变更文件异常: %v", files)
	}
	// 未暂存时 diff 为空，属正常行为。
	diff, truncated, err := f.GitDiff(ctx, root, false, 4096)
	if err != nil {
		t.Fatalf("git diff 失败: %v", err)
	}
	if truncated || strings.Contains(diff, "new.txt") {
		t.Fatalf("未暂存文件不应出现在工作区 diff: %q", diff)
	}
	if _, _, err := f.GitDiff(ctx, root, false, 0); err == nil {
		t.Fatal("非法 maxBytes 必须被拒绝")
	}
}

func TestGit非仓库报错(t *testing.T) {
	root := t.TempDir()
	f := newFiles(t, root)
	if _, err := f.GitStatus(context.Background(), root); err == nil {
		t.Fatal("非仓库必须报错")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := execCommand("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败: %s (%v)", args, out, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

// Test超大目录不整体载入 覆盖 B27：
// 旧实现用 fs.ReadDir 一次性取回整个目录再排序截断，超大单目录
// 会在限额检查之前就占满内存。这里用远超上限的条目数验证仍能正确截断，
// 并且分批读取不会因为 readDir 的 EOF 语义提前结束。
func Test超大目录不整体载入(t *testing.T) {
	root := t.TempDir()
	const total = 3000
	for i := 0; i < total; i++ {
		name := filepath.Join(root, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// 混入目录，验证「目录在前」的排序在截断后依然成立。
	for i := 0; i < 50; i++ {
		if err := os.MkdirAll(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxEntries = 100
	f, err := NewFiles(p, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, truncated, err := f.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 100 {
		t.Fatalf("应按上限截断到 100，实际 %d", len(entries))
	}
	if !truncated {
		t.Fatal("超大目录必须标记截断")
	}
	// 目录优先，且各自按名称有序。
	seenFile := false
	for i, e := range entries {
		if !e.IsDir {
			seenFile = true
			continue
		}
		if seenFile {
			t.Fatalf("第 %d 项目录排在文件之后，排序规则被破坏", i)
		}
		if i > 0 && entries[i-1].IsDir && entries[i-1].Name > e.Name {
			t.Fatalf("目录未按名称有序: %s > %s", entries[i-1].Name, e.Name)
		}
	}
}
