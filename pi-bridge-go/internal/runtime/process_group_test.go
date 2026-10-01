package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// B40：正常停止路径回收整个进程组。
//
// Pdeathsig 只作用于直接子进程；桥被 SIGKILL 时，忽略 SIGTERM 的
// 后代仍会存活——那一半属于「必须有服务管理器按 cgroup 监督」的边界
// （正常 Stop 向整组发信号；SIGKILL 场景由服务管理器的 KillMode=control-group 兜底，
// 见 docs/DEVELOPMENT.md 的「受管部署」）。
// 这里锁住的是桥自己可控的一半：Stop 时向整组（-pid）发信号。
func Test停止回收同组后代(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	m, cwd := newTestManager(t, func(c *Config) {
		c.Env = []string{"FAKE_PI_SCRIPT=spawn_grandchild", "FAKE_PI_CHILD_PID_FILE=" + pidFile}
	})
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	pid := waitPIDFile(t, pidFile)
	if !processAlive(pid) {
		t.Fatalf("后代进程应仍在运行: pid=%d", pid)
	}
	if err := w.Stop(true); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		// 清理：不给后续运行留下孤儿进程。
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("Stop 后同组后代仍存活: pid=%d", pid)
	}
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("夹具未写出后代 PID")
	return 0
}
