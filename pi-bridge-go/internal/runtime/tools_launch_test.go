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
		deny   []string
	}{
		{preset: "", want: nil},
		{preset: ToolPresetDefault, want: nil},
		{preset: ToolPresetChatOnly, want: []string{"--no-tools"}},
		// 只读/完整不再用 CLI 白名单：那会连带禁用扩展工具。
		// 期望通过环境变量把激活名单交给桥内扩展。
		{preset: ToolPresetReadOnly, want: nil, deny: []string{"--tools", "--exclude-tools"}},
		{preset: ToolPresetFull, want: nil, deny: []string{"--tools", "--exclude-tools"}},
	}
	for _, c := range cases {
		t.Run("预设 "+c.preset, func(t *testing.T) {
			args := startWithArgsCapture(t, c.preset)
			for _, want := range c.want {
				if !containsArg(args, want) {
					t.Fatalf("启动参数缺少 %q: %v", want, args)
				}
			}
			// 工具开关必须要么完全不用（改由桥内扩展按名单重设），要么只用 --no-tools。
			for _, arg := range args {
				if arg == "--tools" || arg == "--exclude-tools" {
					t.Fatalf("预设 %q 不应再用 CLI 白名单（会禁用扩展工具）: %v", c.preset, args)
				}
			}
			for _, bad := range c.deny {
				if containsArg(args, bad) {
					t.Fatalf("预设 %q 不应出现 %q: %v", c.preset, bad, args)
				}
			}
		})
	}
}

// Test工具集环境变量随预设下发 断言桥把该预设期望的激活工具名通过
// PI_WEBUI_ACTIVE_TOOLS 交给桥内扩展；default/空预设不下发（沿用 Pi 默认）。
func Test工具集环境变量随预设下发(t *testing.T) {
	cases := []struct {
		preset string
		want   string
	}{
		{preset: ToolPresetChatOnly, want: ""},
		{preset: ToolPresetReadOnly, want: "read,grep,find,ls"},
		{preset: ToolPresetFull, want: "read,bash,edit,write,grep,find,ls"},
	}
	for _, c := range cases {
		t.Run("预设 "+c.preset, func(t *testing.T) {
			env := startWithEnvCapture(t, c.preset, "PI_WEBUI_ACTIVE_TOOLS")
			if env["PI_WEBUI_ACTIVE_TOOLS"] != c.want {
				t.Fatalf("PI_WEBUI_ACTIVE_TOOLS = %q, 期望 %q", env["PI_WEBUI_ACTIVE_TOOLS"], c.want)
			}
		})
	}
	t.Run("默认预设不下发工具集", func(t *testing.T) {
		env := startWithEnvCapture(t, ToolPresetDefault, "PI_WEBUI_ACTIVE_TOOLS")
		if env["PI_WEBUI_ACTIVE_TOOLS"] != "" {
			t.Fatalf("默认预设不应下发 PI_WEBUI_ACTIVE_TOOLS，实际 %q", env["PI_WEBUI_ACTIVE_TOOLS"])
		}
	})
}

// startWithEnvCapture 启动一个 worker，返回假 Pi 记下的点名环境变量。
func startWithEnvCapture(t *testing.T, preset string, keys ...string) map[string]string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "env.txt")
	m, cwd := newTestManager(t, func(cfg *Config) {
		cfg.Env = append(cfg.Env, "FAKE_PI_ENV_FILE="+envFile, "FAKE_PI_ENV_KEYS="+strings.Join(keys, ","))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.StartWithPreset(ctx, "", cwd, preset); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, readErr := os.ReadFile(envFile)
		if readErr == nil {
			out := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				key, value, _ := strings.Cut(line, "=")
				out[key] = value
			}
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("假 Pi 没有写回环境变量: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
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
