package presentation

import (
	"encoding/json"
	"strings"

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
	ID          string
	SessionID   string
	Method      string
	Title       string
	Message     string
	Options     []string
	Placeholder string
	Prefill     string
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
	return DialogData{
		ID:          v.ID,
		SessionID:   sessionID,
		Method:      v.Method,
		Title:       v.Title,
		Message:     v.Message,
		Options:     v.Options,
		Placeholder: v.Placeholder,
		Prefill:     v.Prefill,
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
