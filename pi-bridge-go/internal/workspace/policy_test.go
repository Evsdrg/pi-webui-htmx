package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectory拒绝越界与穿越(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "project")
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := policy.Directory(inside)
	if err != nil || got != inside {
		t.Fatalf("允许的目录被拒绝: %v %v", got, err)
	}
	if _, err := policy.Directory(other); err == nil {
		t.Fatal("越界目录应被拒绝")
	}
	if _, err := policy.Directory(filepath.Join(root, "..", "escape")); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if _, err := policy.Directory("relative/path"); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
	if _, err := policy.Directory(filepath.Join(root, "missing")); err == nil {
		t.Fatal("不存在的目录应被拒绝")
	}
}

func TestNew拒绝文件与空列表(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New([]string{file}); err == nil {
		t.Fatal("文件不应作为工作区根")
	}
	if _, err := New(nil); err == nil {
		t.Fatal("空列表应被拒绝")
	}
}

func Test符号链接逃逸被拒绝(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(other, "target"), link); err != nil {
		t.Skip("跳过：当前环境不支持符号链接")
	}
	policy, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Directory(link); err == nil {
		t.Fatal("指向外部的符号链接应被拒绝")
	}
}
