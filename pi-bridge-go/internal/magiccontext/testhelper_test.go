package magiccontext

import (
	"os/exec"
	"path/filepath"
)

// lookPath 包住 exec.LookPath，让测试能在缺 sqlite3 时跳过而不是失败。
func lookPath(name string) (string, error) { return exec.LookPath(name) }

// runSQLite 建一个临时库。测试不引入 sqlite 驱动——只验证我们的读取路径，
// 建库用系统 sqlite3 完成。
func runSQLite(db, schema string) (string, error) {
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(binary, db, schema)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// 供测试引用 filepath，避免在只有测试用到时的未使用导入。
var _ = filepath.Join
