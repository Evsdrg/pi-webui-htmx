//go:build linux

package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

func TestGit诊断洪峰受限(t *testing.T) {
	root := t.TempDir()
	f := newFiles(t, root)
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nhead -c 1048576 /dev/zero >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f.gitPath = helper
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := f.runGit(ctx, root, 128, io.Discard, "status")
	var apiErr *protocol.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "limit_exceeded" {
		t.Fatalf("诊断洪峰未受限：%v", err)
	}
}

func TestGit取消回收同组后代(t *testing.T) {
	root := t.TempDir()
	f := newFiles(t, root)
	pidFile := filepath.Join(root, "child.pid")
	helper := filepath.Join(t.TempDir(), "helper")
	body := "#!/bin/sh\nsleep 30 &\necho $! > '" + strings.ReplaceAll(pidFile, "'", "'\\''") + "'\nwait\n"
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	f.gitPath = helper
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := f.runGit(ctx, root, 128, io.Discard, "status"); done <- err }()
	pid := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("后代未启动")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消应返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("Git 取消未及时返回")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if os.IsNotExist(err) || strings.Contains(string(data), ") Z ") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Git 同组后代仍在运行")
}
