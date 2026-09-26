package runtime

import (
	"context"
	"encoding/json"
	"sort"

	"pi-bridge-go/internal/protocol"
)

// PendingDialogs 返回仍在等待人工输入的扩展对话。
// PendingDialogs 返回待回复对话的 ID 列表。
func (w *Worker) PendingDialogs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.pendingDialogs))
	for id := range w.pendingDialogs {
		out = append(out, id)
	}
	return out
}

// PendingDialog 返回某个待回复对话的原始载荷。
// HTTP 端点用它渲染对话框；桥不重新解释字段，原样交给呈现层。
func (w *Worker) PendingDialog(id string) (json.RawMessage, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, ok := w.pendingDialogs[id]
	return raw, ok
}

// PendingDialogPayloads 返回全部待回复对话的载荷，按 ID 排序保证稳定。
func (w *Worker) PendingDialogPayloads() []json.RawMessage {
	w.mu.Lock()
	ids := make([]string, 0, len(w.pendingDialogs))
	for id := range w.pendingDialogs {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	sort.Strings(ids)
	out := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		if raw, ok := w.PendingDialog(id); ok {
			out = append(out, raw)
		}
	}
	return out
}

// UIResponse 把前端的选择回传给扩展。
// value/confirmed/cancelled 三种语义对应 Pi 的 dialog 方法：
//   - select/input/editor 用 value
//   - confirm 用 confirmed
//   - 任意 dialog 都可以用 cancelled 取消
func (w *Worker) UIResponse(ctx context.Context, id string, value *string, confirmed *bool, cancelled bool) error {
	if id == "" {
		return protocol.E("invalid_params", "id 不能为空")
	}
	w.mu.Lock()
	_, tracked := w.pendingDialogs[id]
	if tracked {
		delete(w.pendingDialogs, id)
		if len(w.pendingDialogs) == 0 {
			w.waitingInput = false
			if !w.active && !w.queued {
				w.status = "idle"
			}
		}
	}
	w.mu.Unlock()
	if !tracked {
		return protocol.E("not_found", "没有该 ID 的待回复对话")
	}
	payload := map[string]any{"type": "extension_ui_response", "id": id}
	switch {
	case cancelled:
		payload["cancelled"] = true
	case confirmed != nil:
		payload["confirmed"] = *confirmed
	case value != nil:
		payload["value"] = *value
	default:
		return protocol.E("invalid_params", "必须提供 value、confirmed 或 cancelled 之一")
	}
	// 回执必须送达：Notify 现在返回错误，连接已断时不再静默丢弃。
	if err := w.client.Notify(payload); err != nil {
		return protocol.E("worker_exited", "无法送达扩展回执：工作进程连接已断开")
	}
	w.mu.Lock()
	w.lastActivity = nowUTC()
	w.mu.Unlock()
	return nil
}

// CancelPendingDialogs 取消全部未回复对话。
// 用于 worker 停止或连接断开后的清理，避免扩展永久挂起。
func (w *Worker) CancelPendingDialogs() {
	w.mu.Lock()
	ids := make([]string, 0, len(w.pendingDialogs))
	for id := range w.pendingDialogs {
		ids = append(ids, id)
	}
	w.pendingDialogs = map[string]json.RawMessage{}
	w.waitingInput = false
	w.mu.Unlock()
	for _, id := range ids {
		// 连接已断时无需也无法回执，忽略错误即可。
		_ = w.client.Notify(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	}
}

// DialogRequest 是转发给前端的扩展对话载荷。
type DialogRequest struct {
	ID          string   `json:"id"`
	Method      string   `json:"method"`
	Title       string   `json:"title,omitempty"`
	Message     string   `json:"message,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Prefill     string   `json:"prefill,omitempty"`
	Options     []string `json:"options,omitempty"`
	TimeoutMs   int      `json:"timeout,omitempty"`
}

// ParseDialog 从原始事件解析对话请求；非对话事件返回 false。
func ParseDialog(raw json.RawMessage) (DialogRequest, bool) {
	var ev struct {
		Type        string   `json:"type"`
		ID          string   `json:"id"`
		Method      string   `json:"method"`
		Title       string   `json:"title"`
		Message     string   `json:"message"`
		Placeholder string   `json:"placeholder"`
		Prefill     string   `json:"prefill"`
		Options     []string `json:"options"`
		Timeout     int      `json:"timeout"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "extension_ui_request" || ev.ID == "" {
		return DialogRequest{}, false
	}
	return DialogRequest{
		ID: ev.ID, Method: ev.Method, Title: ev.Title, Message: ev.Message,
		Placeholder: ev.Placeholder, Prefill: ev.Prefill, Options: ev.Options,
		TimeoutMs: ev.Timeout,
	}, true
}
