package presentation

import (
	"encoding/json"
	"sort"
	"strings"

	"pi-bridge-go/internal/protocol"
)

// 通用扩展通道的呈现。
//
// 前端不针对任何具体插件写代码。全部走这三张模板：
//   - status.html  对应 setStatus（fire-and-forget）
//   - widgets.html 对应 setWidget（fire-and-forget）
//   - dialog.html  对应 select/confirm/input/editor（需回执）
//
// 与 Pi RPC 模式的对应关系见 pi-webui-htmx/ui-manifest.json 的
// extensionChannel 段，以及 docs/pi-compatibility.md。

// StatusItem 是一个插件的状态行。
type StatusItem struct {
	Key  string
	Text string
}

// StatusData 驱动状态行模板。
type StatusData struct {
	Statuses []StatusItem
}

// RenderExtensionStatus 渲染扩展状态行。
//
// 当前实现：状态来自 WS 推送的 extension_ui_request（setStatus），
// 由客户端直接更新 DOM；这个 HTTP 端点只在页面初次加载时给一个占位。
// 后续若要做「刷新后仍在」，需要桥侧维护一个有界的最近状态表。
func (r *Renderer) RenderExtensionStatus(items []StatusItem) (string, error) {
	// 按 key 去重并排序，保证多次渲染间稳定不跳动。
	seen := map[string]string{}
	for _, it := range items {
		if it.Key == "" {
			continue
		}
		seen[it.Key] = it.Text
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]StatusItem, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, StatusItem{Key: k, Text: seen[k]})
	}
	return r.execute("extensions/status.html", StatusData{Statuses: rows})
}

// WidgetItem 是一个扩展小组件。
// Pi 的 RPC 模式只接收字符串数组，工厂函数会被丢弃，所以这里只有行。
type WidgetItem struct {
	Key       string
	Title     string
	Lines     []string
	Placement string
}

// WidgetsData 驱动小组件模板。
type WidgetsData struct {
	Widgets []WidgetItem
}

// RenderExtensionWidgets 渲染扩展小组件。
func (r *Renderer) RenderExtensionWidgets(items []WidgetItem) (string, error) {
	return r.execute("extensions/widgets.html", WidgetsData{Widgets: items})
}

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
