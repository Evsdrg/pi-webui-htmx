package runtime

import (
	"strings"
	"testing"
)

func TestToolPresetArgs映射(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		want   []string
		deny   bool
	}{
		{name: "空预设沿用 Pi 默认", preset: "", want: nil},
		{name: "default 不加参数", preset: ToolPresetDefault, want: nil},
		{name: "chat-only 禁用全部工具", preset: ToolPresetChatOnly, want: []string{"--no-tools"}},
		// 只读与完整不再用 CLI 白名单：Pi 的 --tools/--exclude-tools 会连带禁用扩展工具，
		// 改由桥内扩展在会话启动时按名单重设激活工具（见 ToolPresetActiveTools）。
		{name: "read-only 不用 CLI 白名单", preset: ToolPresetReadOnly, want: nil},
		{name: "full 不用 CLI 白名单", preset: ToolPresetFull, want: nil},
		{name: "未知预设必须拒绝", preset: "admin", deny: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ToolPresetArgs(c.preset)
			if c.deny {
				if err == nil {
					t.Fatalf("未知预设应被拒绝: %q", c.preset)
				}
				return
			}
			if err != nil {
				t.Fatalf("预设 %q 不应失败: %v", c.preset, err)
			}
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("预设 %q 参数 = %q, 期望 %q", c.preset, got, c.want)
			}
		})
	}
}

func TestValidToolPreset(t *testing.T) {
	for _, ok := range []string{"", ToolPresetDefault, ToolPresetChatOnly, ToolPresetReadOnly, ToolPresetFull} {
		if !ValidToolPreset(ok) {
			t.Fatalf("预设 %q 应被接受", ok)
		}
	}
	for _, bad := range []string{"full ", "chat", "read", "admin", "none"} {
		if ValidToolPreset(bad) {
			t.Fatalf("预设 %q 不应被接受", bad)
		}
	}
}

// TestToolPresetActiveTools 钉住每个预设期望的内置工具集（桥内扩展据此重设激活工具）。
// grep/find/ls 在 Pi 里默认关闭，只读与完整都必须显式把它们列进来；
// 扩展工具不由这里给出，由扩展自身并入，避免重名覆盖面。
// chat-only 走 CLI --no-tools（同时禁用内置与扩展工具，与 Pi Web 一致），
// 不读本名单；default/空预设保持 Pi 默认，也不干预。
func TestToolPresetActiveTools(t *testing.T) {
	cases := []struct {
		preset string
		want   string
		ok     bool
	}{
		{preset: ToolPresetDefault, ok: false},
		{preset: "", ok: false},
		{preset: ToolPresetChatOnly, ok: false},
		{preset: ToolPresetReadOnly, want: "read,grep,find,ls", ok: true},
		{preset: ToolPresetFull, want: "read,bash,edit,write,grep,find,ls", ok: true},
		{preset: "admin", ok: false},
	}
	for _, c := range cases {
		name := c.preset
		if name == "" {
			name = "(空)"
		}
		t.Run(name, func(t *testing.T) {
			got, ok := ActiveToolsForPreset(c.preset)
			if ok != c.ok {
				t.Fatalf("ActiveToolsForPreset(%q) ok = %v, 期望 %v", c.preset, ok, c.ok)
			}
			if ok && strings.Join(got, ",") != c.want {
				t.Fatalf("ActiveToolsForPreset(%q) = %q, 期望 %q", c.preset, strings.Join(got, ","), c.want)
			}
		})
	}
}
