// Package testutil 提供跨包复用的测试辅助。
//
// 存在的理由：多个包的测试都需要一个「可控的假 Pi」，
// 而假 Pi 是一段需要编译的 main 程序。此前各包直接引用
// testdata/bin/fake-pi 这个编译产物，但它被 .gitignore——
// 新克隆的仓库 go test 必然失败，且没有任何构建脚本兜底。
//
// 现在由本包统一负责：测试启动时按需构建到临时目录，
// 测试自包含，不依赖预编译产物，也不把二进制提交进仓库。
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	once     sync.Once
	binary   string
	buildErr error
)

// FakePi 返回假 Pi 可执行文件的路径，必要时先构建它。
//
// 构建目标放在 os.TempDir() 下而不是仓库内的 testdata/bin/：
//   - 避免把编译产物写进工作区，也避免污染 git status
//   - 同一测试进程内由 sync.Once 复用；不同包/checkout 使用独占目录
//   - 使用假 Pi 的包必须在 TestMain 调用 Run，全部测试退出后统一清理
//
// 构建失败时返回错误，调用方应直接让测试失败——
// 静默跳过会让「测试通过」变成假象。
func FakePi() (string, error) {
	once.Do(func() {
		binary, buildErr = build()
	})
	return binary, buildErr
}

// Run 执行测试并清理本进程的假 Pi；应由使用 FakePi 的包在 TestMain 调用。
// m.Run 返回前测试的 Cleanup 已执行，worker 已停止，不提前删除共享产物。
func Run(m *testing.M) int {
	code := m.Run()
	if binary != "" {
		if err := os.RemoveAll(filepath.Dir(binary)); err != nil {
			fmt.Fprintln(os.Stderr, "清理假 Pi 临时目录失败：", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

// build 编译 testdata/fake-pi 到本次构建独占的临时目录。
func build() (string, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	return buildIn(repoRoot)
}

func buildIn(repoRoot string) (string, error) {
	dir, err := os.MkdirTemp("", "pi-bridge-fake-pi-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "fake-pi")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fake-pi")
	cmd.Dir = repoRoot
	if b, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", &BuildError{Output: string(b), Err: err}
	}
	return out, nil
}

// findRepoRoot 从当前工作目录向上找到含 go.mod 的目录。
// 测试的工作目录是包所在目录（如 internal/runtime），需要回溯到仓库根。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// BuildError 描述假 Pi 构建失败。
type BuildError struct {
	Output string
	Err    error
}

func (e *BuildError) Error() string {
	return "构建假 Pi 失败: " + e.Err.Error() + "\n" + e.Output
}

func (e *BuildError) Unwrap() error { return e.Err }
