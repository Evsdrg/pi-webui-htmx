package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startWithArgsCapture 启动一个 worker，并把假 Pi 收到的启动参数写到文件。
func startWithArgsCapture(t *testing.T) []string {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m, cwd := newTestManager(t, func(cfg *Config) {
		cfg.Env = append(cfg.Env, "FAKE_PI_ARGS_FILE="+argsFile)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Start(ctx, "", cwd); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, readErr := os.ReadFile(argsFile)
		if readErr == nil && len(raw) > 0 {
			return strings.Fields(strings.ReplaceAll(string(raw), "\n", " "))
		}
		if time.Now().After(deadline) {
			t.Fatalf("假 Pi 没有写回启动参数: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Test启动不带工具开关 钉住「工作进程始终启用全部工具、不用 CLI 工具开关」。
// Pi 的 --tools 白名单会连带禁用扩展工具、--exclude-tools 无法额外启用，
// 因此工具集完全交给桥内扩展（tools-ext.mjs）。
func Test启动不带工具开关(t *testing.T) {
	args := startWithArgsCapture(t)
	for _, arg := range args {
		if arg == "--tools" || arg == "--exclude-tools" || arg == "--no-tools" {
			t.Fatalf("启动不应带工具开关（工具集由桥内扩展施加）: %v", args)
		}
	}
}

// Test工具集扩展随进程下发 断言桥把 tools-ext.mjs 以 -e 下发给 Pi。
func Test工具集扩展随进程下发(t *testing.T) {
	args := startWithArgsCapture(t)
	var sawExt bool
	for i, arg := range args {
		if arg == "-e" && i+1 < len(args) && strings.HasSuffix(args[i+1], "bridge-tools.mjs") {
			sawExt = true
		}
	}
	if !sawExt {
		t.Fatalf("启动参数应包含桥内工具集扩展 -e .../bridge-tools.mjs: %v", args)
	}
}
