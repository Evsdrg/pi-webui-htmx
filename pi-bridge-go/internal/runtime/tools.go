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
// 只有 chat-only 需要 CLI 参数（--no-tools 同时禁用内置与扩展工具，与 Pi Web 一致）。
// 其余预设不在这里用 --tools / --exclude-tools：
//   - Pi 的 --tools 是“只允许这些工具”的白名单，会**连带禁用扩展工具**
//     （magic-context / pi-goal-x 的 ctx_* / goal 工具会一起消失）；
//   - --exclude-tools 是 deny 列表，无法用来“额外启用默认关闭的 grep/find/ls”。
//
// 因此 read-only / full 的“启用哪些内置工具”改由桥内工具集扩展在会话启动时
// 通过 pi.setActiveTools() 施加（见 ActiveToolsForPreset 与 tools-ext.mjs）：
// 扩展把预设名单与当前所有扩展工具并集后重设，既不丢插件工具，又能启用 grep/find/ls。
func ToolPresetArgs(preset string) ([]string, error) {
	switch preset {
	case "", ToolPresetDefault:
		// Pi 默认启用 read/bash/edit/write，扩展工具照常可用。
		return nil, nil
	case ToolPresetChatOnly:
		return []string{"--no-tools"}, nil
	case ToolPresetReadOnly, ToolPresetFull:
		// 名单经 PI_WEBUI_ACTIVE_TOOLS 交给桥内扩展；CLI 不掺工具开关。
		return nil, nil
	}
	return nil, protocol.E("invalid_params", "未知的工具预设")
}

// ActiveToolsForPreset 返回该预设期望激活的**内置**工具名单，以及是否需要施加。
//
// 返回 ok=false 表示不改动 Pi 的默认工具集（空预设与 default：Pi 默认
// read/bash/edit/write + 扩展工具，无需干预）。ok=true 时名单会经
// PI_WEBUI_ACTIVE_TOOLS 下发给桥内扩展；chat-only 给空名单（仍保留扩展工具，
// 与 --no-tools 的“全禁用”不同——但 chat-only 走 CLI --no-tools，不读本名单）。
//
// 扩展工具不在名单内：它们由扩展自行并入（重名时以扩展工具为准），
// 这样桥不需要枚举插件注册了哪些工具，也不会因为插件升级而失配。
func ActiveToolsForPreset(preset string) ([]string, bool) {
	switch preset {
	case ToolPresetReadOnly:
		return []string{"read", "grep", "find", "ls"}, true
	case ToolPresetFull:
		return []string{"read", "bash", "edit", "write", "grep", "find", "ls"}, true
	}
	return nil, false
}

// joinToolNames 供扩展环境变量使用：逗号分隔，保持稳定顺序。
func joinToolNames(names []string) string { return strings.Join(names, ",") }
