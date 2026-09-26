package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
