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
func startWithArgsCapture(t *testing.T, preset string) []string {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	m, cwd := newTestManager(t, func(cfg *Config) {
		cfg.Env = append(cfg.Env, "FAKE_PI_ARGS_FILE="+argsFile)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := m.StartWithPreset(ctx, "", cwd, preset)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, readErr := os.ReadFile(argsFile)
		if readErr == nil && len(raw) > 0 {
			_ = w
			return strings.Fields(strings.ReplaceAll(string(raw), "\n", " "))
		}
		if time.Now().After(deadline) {
			t.Fatalf("假 Pi 没有写回启动参数: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func Test工具预设进入Pi启动参数(t *testing.T) {
	cases := []struct {
		preset string
		want   []string
	}{
		{preset: "", want: nil},
		{preset: ToolPresetDefault, want: nil},
		{preset: ToolPresetChatOnly, want: []string{"--no-tools"}},
		{preset: ToolPresetReadOnly, want: []string{"--exclude-tools", "bash,edit,write"}},
		{preset: ToolPresetFull, want: []string{"--tools", "bash,read,edit,write,grep,find,ls"}},
	}
	for _, c := range cases {
		t.Run("预设 "+c.preset, func(t *testing.T) {
			args := startWithArgsCapture(t, c.preset)
			for _, want := range c.want {
				if !containsArg(args, want) {
					t.Fatalf("启动参数缺少 %q: %v", want, args)
				}
			}
			// default/空预设不应掺入任何工具开关。
			if len(c.want) == 0 {
				for _, arg := range args {
					if arg == "--tools" || arg == "--exclude-tools" || arg == "--no-tools" {
						t.Fatalf("默认预设不应带工具开关: %v", args)
					}
				}
			}
		})
	}
}

// containsArg 判断 args 中是否出现 want。
func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func Test未知预设不会启动进程(t *testing.T) {
	m, cwd := newTestManager(t)
	_, err := m.StartWithPreset(context.Background(), "", cwd, "admin")
	if err == nil {
		t.Fatal("未知预设应被拒绝")
	}
	if !strings.Contains(err.Error(), "工具预设") {
		t.Fatalf("错误信息应说明原因: %v", err)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("拒绝后不应留下工作进程: %d", got)
	}
}
