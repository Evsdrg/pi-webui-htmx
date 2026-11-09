package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 上传（写）能力的第一道边界：路径必须落在授权根内，且不得经过符号链接逃逸。
// 这些用例与读路径共用 resolve，但写路径多一条「目标不能已存在」——
// 读错一个路径只是看到别的文件，写错一个路径会覆盖用户的源码。

func newUploadFiles(t *testing.T) (*Files, string) {
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
	return f, root
}

func Test上传写入根内新文件(t *testing.T) {
	f, root := newUploadFiles(t)
	target := filepath.Join(root, "notes.txt")
	n, err := f.Upload(strings.NewReader("hello 世界"), target, 0o644)
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	if n != int64(len("hello 世界")) {
		t.Fatalf("返回字节数 %d，期望 %d", n, len("hello 世界"))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello 世界" {
		t.Fatalf("内容不符：%q", got)
	}
}

func Test上传拒绝覆盖已存在文件(t *testing.T) {
	f, root := newUploadFiles(t)
	target := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(target, []byte("原件"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.Upload(strings.NewReader("新内容"), target, 0o644)
	if err == nil {
		t.Fatal("覆盖已存在文件应当被拒绝")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "原件" {
		t.Fatalf("原文件被改动：%q", got)
	}
}

func Test上传拒绝根外路径(t *testing.T) {
	f, _ := newUploadFiles(t)
	outside := filepath.Join(t.TempDir(), "escape.txt")
	if _, err := f.Upload(strings.NewReader("x"), outside, 0o644); err == nil {
		t.Fatal("根外路径应当被拒绝")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("根外文件不应被创建")
	}
}

// 符号链接逃逸：目标是链接时，写穿过去就会落到根外。
// 用 os.Root 的 O_CREATE|O_EXCL 本身就会拒绝（链接已存在），
// 但这条用例钉住的是「不跟随最后一段符号链接」这个语义。
func Test上传不跟随符号链接逃逸(t *testing.T) {
	f, root := newUploadFiles(t)
	outsideDir := t.TempDir()
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(filepath.Join(outsideDir, "stolen.txt"), link); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}
	if _, err := f.Upload(strings.NewReader("x"), link, 0o644); err == nil {
		t.Fatal("经符号链接上传应当被拒绝")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "stolen.txt")); err == nil {
		t.Fatal("根外文件不应被创建")
	}
}

func Test上传拒绝相对路径(t *testing.T) {
	f, _ := newUploadFiles(t)
	if _, err := f.Upload(strings.NewReader("x"), "relative.txt", 0o644); err == nil {
		t.Fatal("相对路径应当被拒绝")
	}
}

func Test上传超限时不留半截文件(t *testing.T) {
	f, root := newUploadFiles(t)
	// 限额 4 MiB：给 4 MiB + 1 字节。
	payload := bytes.Repeat([]byte("a"), int(DefaultLimits().MaxReadByte)+1)
	target := filepath.Join(root, "big.bin")
	if _, err := f.Upload(bytes.NewReader(payload), target, 0o644); err == nil {
		t.Fatal("超限上传应当被拒绝")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("超限上传不应留下文件（半截文件比失败更糟）")
	}
}

func Test上传父目录不存在时报错(t *testing.T) {
	f, root := newUploadFiles(t)
	target := filepath.Join(root, "no-such-dir", "a.txt")
	if _, err := f.Upload(strings.NewReader("x"), target, 0o644); err == nil {
		t.Fatal("父目录不存在应当报错，而不是自动创建")
	}
}

// 工作区根被改名重建后，缓存的根句柄仍指向旧 inode：
// 路径校验看到的是新目录，写入却落进已消失的旧目录（独立复审实测复现）。
func Test上传在工作区根重建后不得写进旧目录(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFiles(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	old := root + "-old"
	if err := os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Upload(strings.NewReader("x"), filepath.Join(root, "new.txt"), 0o644); err != nil {
		t.Logf("上传返回错误（可接受）：%v", err)
	}
	if _, serr := os.Stat(filepath.Join(old, "new.txt")); serr == nil {
		t.Fatalf("写进了被删掉的旧工作区根（stale inode）：%s", old)
	}
	if _, serr := os.Stat(filepath.Join(root, "new.txt")); serr != nil {
		t.Fatalf("文件应落在重建后的根内：%v", serr)
	}
}

func mustTestPolicy(t *testing.T, root string) *Policy {
	t.Helper()
	p, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
