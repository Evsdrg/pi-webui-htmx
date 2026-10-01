package runtime

import (
	"strings"

	"pi-bridge-go/internal/protocol"
)

// 工具预设名与 Pi Web（lib/tool-presets.ts）的四个选项对齐。
const (
	ToolPresetChatOnly = "chat-only"
	ToolPresetReadOnly = "read-only"
	ToolPresetDefault  = "default"
	ToolPresetFull     = "full"
)

// builtinToolNames 是 Pi 内置编码工具全集，与 Pi Web 的 PRESET_FULL 一致。
// powershell 只在 Windows 上注册，这里不列入：桥按本机平台由 Pi 自行决定。
var builtinToolNames = []string{"bash", "read", "edit", "write", "grep", "find", "ls"}

// ValidToolPreset 判断是否是受支持的工具预设。
func ValidToolPreset(preset string) bool {
	switch preset {
	case "", ToolPresetDefault, ToolPresetChatOnly, ToolPresetReadOnly, ToolPresetFull:
		return true
	}
	return false
}

// ToolPresetArgs 把工具预设翻译成 Pi CLI 参数。
//
// 取值语义与 Pi Web 对齐：预设只约束内置编码工具，扩展工具保持可用。
// 但 Pi 的 --tools 是“只允许这些工具”的白名单，会连带禁用扩展工具，
// 因此 full 无法既启用 grep/find/ls 又保留扩展工具——这是上游限制，
// 调用方必须把这一代价告知用户（见 protocol 文档）。
func ToolPresetArgs(preset string) ([]string, error) {
	switch preset {
	case "", ToolPresetDefault:
		// Pi 默认启用 read/bash/edit/write，扩展工具照常可用，与 default 预设一致。
		return nil, nil
	case ToolPresetChatOnly:
		// --no-tools 同时禁用内置与扩展工具，与 Pi Web 的 chat-only 一致。
		return []string{"--no-tools"}, nil
	case ToolPresetReadOnly:
		// 只排除写操作，read/grep/find/ls 与扩展工具保持可用。
		return []string{"--exclude-tools", strings.Join([]string{"bash", "edit", "write"}, ",")}, nil
	case ToolPresetFull:
		// 需要白名单才能启用 grep/find/ls；代价是扩展工具被一并禁用。
		return []string{"--tools", strings.Join(builtinToolNames, ",")}, nil
	}
	return nil, protocol.E("invalid_params", "未知的工具预设")
}
