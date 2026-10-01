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
		{name: "read-only 只排除写操作", preset: ToolPresetReadOnly, want: []string{"--exclude-tools", "bash,edit,write"}},
		{name: "full 需要白名单", preset: ToolPresetFull, want: []string{"--tools", "bash,read,edit,write,grep,find,ls"}},
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
