package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"time"

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
//
// 顺序很关键：必须先校验参数再摘除对话。反过来会让一次非法回执把对话
// 从表里删掉，用户无法修正重试，而 Pi 仍在等待（B16）。
func (w *Worker) UIResponse(ctx context.Context, id string, value *string, confirmed *bool, cancelled bool) error {
	if id == "" {
		return protocol.E("invalid_params", "id 不能为空")
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
		// 参数不合法时对话必须留在表里，否则合法重试会变成 not_found。
		return protocol.E("invalid_params", "必须提供 value、confirmed 或 cancelled 之一")
	}
	w.mu.Lock()
	raw, tracked := w.pendingDialogs[id]
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
	// 回执必须送达：Notify 现在返回错误，连接已断时不再静默丢弃。
	if err := w.client.Notify(payload); err != nil {
		// 送达失败要把对话还回去：否则前端以为已回复，Pi 却永远等不到。
		w.restoreDialog(id, raw)
		return protocol.E("worker_exited", "无法送达扩展回执：工作进程连接已断开")
	}
	w.mu.Lock()
	w.lastActivity = nowUTC()
	w.mu.Unlock()
	return nil
}

// restoreDialog 把一条被摘除的对话放回等待表。
// 用于回执送达失败后的补偿：宁可让用户再试一次，也不能让 Pi 空等。
func (w *Worker) restoreDialog(id string, raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.pendingDialogs[id]; exists {
		return
	}
	if len(w.pendingDialogs) >= w.cfg.MaxDialogs {
		return
	}
	w.pendingDialogs[id] = raw
	w.waitingInput = true
	if !w.active && !w.queued {
		w.status = "waiting_input"
	}
}

// ExpireDialogs 清理已超过自身 timeout 的对话。
// Pi 在 RPC 模式下到期会自行解决并删除 pending 请求，不会通知桥；
// 桥若不清理，waitingInput 永远为真，worker 失去空闲回收资格（B48）。
func (w *Worker) ExpireDialogs(now time.Time) int {
	w.mu.Lock()
	expired := make([]string, 0, 4)
	for id, raw := range w.pendingDialogs {
		req, ok := ParseDialog(raw)
		if !ok || req.TimeoutMs <= 0 {
			continue
		}
		if now.Sub(w.dialogOpened[id]) < time.Duration(req.TimeoutMs)*time.Millisecond {
			continue
		}
		expired = append(expired, id)
	}
	for _, id := range expired {
		delete(w.pendingDialogs, id)
		delete(w.dialogOpened, id)
	}
	if len(w.pendingDialogs) == 0 {
		w.waitingInput = false
		if !w.active && !w.queued {
			w.status = "idle"
		}
	}
	w.mu.Unlock()
	// Pi 已自行解决，不需要也不应该再回执；只通知前端撤掉对话框。
	for _, id := range expired {
		w.mu.Lock()
		w.publishLocked("bridge.dialog_expired", map[string]any{"id": id})
		w.mu.Unlock()
	}
	return len(expired)
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
