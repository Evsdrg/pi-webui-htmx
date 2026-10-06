package presentation

import (
	"encoding/json"
	"strings"

	"pi-bridge-go/internal/goal"
	"pi-bridge-go/internal/protocol"
)

// 通用扩展通道的呈现（需回执的一类）。
//
// 前端不针对任何具体插件写代码。这里只有对话框模板：
//   - dialog.html  对应 select/confirm/input/editor（需回执）
//
// setStatus（状态行）与 setWidget（小组件）是 fire-and-forget：它们经 WS 直接
// 送到客户端由前端更新 DOM，不经过服务端渲染（原先各有一张模板与一个状态端点，
// 前端改走 RPC session.ext_status 后即成死代码，已删除）。
//
// 与 Pi RPC 模式的对应关系见 pi-webui-htmx/ui-manifest.json 的
// extensionChannel 段。

// DialogData 驱动对话框模板。
// 覆盖 select/confirm/input/editor 四种，模板按 Method 分支渲染。
type DialogData struct {
	ID        string
	SessionID string
	Method    string
	Title     string
	Message   string
	Options   []string
	// OptionLabels 与 Options 一一对应，是**显示文本**（可能已汉化）；
	// 选项的 value 仍是 Options 里的原文——插件靠比较回传值判定用户选了
	// 什么，把回传值也换成译文会让选择静默失效。
	OptionLabels []string
	Placeholder  string
	Prefill      string
}

// RenderExtensionDialog 渲染扩展对话。
func (r *Renderer) RenderExtensionDialog(d DialogData) (string, error) {
	return r.execute("extensions/dialog.html", d)
}

// DialogFromPi 把 Pi 的 extension_ui_request 载荷转成对话框数据。
//
// 桥不重新解释语义，只做字段搬运；模板按 Method 分支渲染。
// Pi 的载荷字段见 packages/coding-agent/src/modes/rpc/rpc-mode.ts 的
// createExtensionUIContext。
func DialogFromPi(id, sessionID string, raw json.RawMessage) (DialogData, error) {
	var v struct {
		ID          string   `json:"id"`
		Method      string   `json:"method"`
		Title       string   `json:"title"`
		Message     string   `json:"message"`
		Options     []string `json:"options"`
		Placeholder string   `json:"placeholder"`
		Prefill     string   `json:"prefill"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return DialogData{}, protocol.E("invalid_params", "对话载荷不是合法 JSON")
	}
	if v.ID == "" {
		v.ID = id
	}
	if v.ID == "" {
		return DialogData{}, protocol.E("invalid_params", "对话缺少 id")
	}
	// 只接受四类需要回执的方法；其余是 fire-and-forget，
	// 不该出现在待回复列表里。
	switch v.Method {
	case "select", "confirm", "input", "editor":
	default:
		return DialogData{}, protocol.E("invalid_params", "该方法不需要对话框回执: "+v.Method)
	}
	// options 只保留字符串，且限制数量，防止恶意插件撑爆渲染。
	if len(v.Options) > 64 {
		v.Options = v.Options[:64]
	}
	// 显示文本单独算一遍：命中的（如 goal 插件的选项）译成中文，
	// 未命中的原样。value 始终用原文。
	labels := make([]string, len(v.Options))
	for i, option := range v.Options {
		if label, ok := goal.OptionLabel(option); ok {
			labels[i] = label
		} else {
			labels[i] = option
		}
	}
	return DialogData{
		ID:           v.ID,
		SessionID:    sessionID,
		Method:       v.Method,
		Title:        v.Title,
		Message:      v.Message,
		Options:      v.Options,
		OptionLabels: labels,
		Placeholder:  v.Placeholder,
		Prefill:      v.Prefill,
	}, nil
}

// RenderExtensionDialogs 渲染多个对话框。
func (r *Renderer) RenderExtensionDialogs(dialogs []DialogData) (string, error) {
	var b strings.Builder
	for _, d := range dialogs {
		html, err := r.execute("extensions/dialog.html", d)
		if err != nil {
			return "", err
		}
		b.WriteString(html)
	}
	return b.String(), nil
}
