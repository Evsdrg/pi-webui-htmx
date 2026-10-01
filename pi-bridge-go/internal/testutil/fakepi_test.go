package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(Run(m))
}

func TestFakePi独立构建不会覆盖彼此(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			path, err := build()
			results <- result{path, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil {
		t.Fatalf("并发构建失败：%v / %v", a.err, b.err)
	}
	if filepath.Dir(a.path) == filepath.Dir(b.path) {
		t.Fatal("独立构建共用了产物目录")
	}
	for _, p := range []string{a.path, b.path} {
		info, err := os.Stat(filepath.Dir(p))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("产物目录应为私有目录：%v %v", info, err)
		}
	}
	if err := os.RemoveAll(filepath.Dir(a.path)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.path); err != nil {
		t.Fatalf("清理一个构建影响了另一个构建：%v", err)
	}
}

func TestFakePi失败保留编译诊断并清理目录(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "testdata", "fake-pi"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"go.mod":                   "module broken\n\ngo 1.20\n",
		"testdata/fake-pi/main.go": "package main\nfunc main() { missingSymbol() }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifacts := t.TempDir()
	t.Setenv("TMPDIR", artifacts)
	path, err := buildIn(root)
	var buildErr *BuildError
	if path != "" || !errors.As(err, &buildErr) || !strings.Contains(buildErr.Output, "missingSymbol") {
		t.Fatalf("未返回真实编译诊断：path=%q err=%v", path, err)
	}
	entries, err := os.ReadDir(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pi-bridge-fake-pi-") {
			t.Fatalf("构建失败后留下目录：%s", entry.Name())
		}
	}
}

func TestFakePi构建并可执行(t *testing.T) {
	p, err := FakePi()
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("产物不存在: %v", err)
	}
	// 必须是可执行文件，不能是目录或空文件。
	if info.IsDir() || info.Size() == 0 {
		t.Fatalf("产物异常: %s (%d bytes)", p, info.Size())
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("产物没有执行位: %s", p)
	}
}

func TestFindRepoRoot向上查找goMod(t *testing.T) {
	// 测试的工作目录是包目录，必须能回溯到仓库根。
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("找不到仓库根: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("返回的不是仓库根: %s", root)
	}
}

func TestBuildError带出编译输出(t *testing.T) {
	// 错误信息必须包含底层输出，否则「构建失败」无从排查。
	e := &BuildError{Output: "some compiler output", Err: os.ErrPermission}
	if !strings.Contains(e.Error(), "some compiler output") {
		t.Fatalf("错误信息丢了编译输出: %s", e.Error())
	}
	if e.Unwrap() != os.ErrPermission {
		t.Fatal("Unwrap 应返回底层错误")
	}
}
