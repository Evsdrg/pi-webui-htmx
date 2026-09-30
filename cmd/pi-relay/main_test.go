package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B55：设备注册表与用户表是持久身份，默认目录不能落在系统临时目录。
// 老默认是 os.TempDir()/pi-relay：重启或清理 /tmp 后注册表整体消失，
// 已配对的设备需要重新登记。
func Test默认状态目录是持久位置(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	if got := defaultStateDir(); got != filepath.Join("/xdg-state", "pi-relay") {
		t.Fatalf("应优先使用 XDG_STATE_HOME: %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无家目录，跳过")
	}
	got := defaultStateDir()
	if !strings.HasPrefix(got, home) {
		t.Fatalf("应落在用户目录下: %q", got)
	}
	if strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("默认目录不得位于临时目录（B55）: %q", got)
	}
}
