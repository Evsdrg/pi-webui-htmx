package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/sessions"
)

// Rebind 把工作进程在进程表里的键从旧会话 ID 改成新会话 ID。
// 必须在使用方确认 Pi 已切换到新身份之后调用，且只允许 owner 调用，
// 避免并发请求下两个 ID 指向同一进程。
func (m *Manager) Rebind(w *Worker, newID string) error {
	if !sessions.ValidID(newID) {
		return protocol.E("pi_error", "Pi 返回的新会话 ID 无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return protocol.E("worker_exited", "桥正在关闭")
	}
	w.mu.Lock()
	old := w.id
	w.mu.Unlock()
	if old == newID {
		return nil
	}
	if existing := m.workers[newID]; existing != nil && existing != w {
		return protocol.E("conflict", "目标会话已有工作进程")
	}
	if current := m.workers[old]; current == w {
		delete(m.workers, old)
	}
	w.mu.Lock()
	w.id = newID
	w.uncertain = false
	w.lastActivity = time.Now()
	w.mu.Unlock()
	m.workers[newID] = w
	// 会话身份变化后，旧 epoch 的补发缓冲立即失效。
	w.resetReplay()
	return nil
}

// NewSession 让 Pi 开启一个新会话，并把进程表重绑定到新 ID。
func (w *Worker) NewSession(ctx context.Context, parentSession string) (string, error) {
	fields := map[string]any{}
	if parentSession != "" {
		fields["parentSession"] = parentSession
	}
	raw, err := w.call(ctx, "new_session", fields, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Cancelled bool   `json:"cancelled"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", protocol.E("pi_error", "Pi 返回的新建会话结果无效")
	}
	if out.Cancelled {
		return "", protocol.E("conflict", "扩展取消了新建会话")
	}
	if out.SessionID == "" {
		// 新版可能不回 sessionId，用状态查询兜底。
		state, err := w.stateAfterRebind(ctx)
		if err != nil {
			return "", err
		}
		out.SessionID = state.SessionID
	}
	if err := w.owner.Rebind(w, out.SessionID); err != nil {
		return "", err
	}
	return out.SessionID, nil
}

// SwitchSession 切换到受管目录内的另一个会话文件。
// 外部路径一律拒绝，避免借切换读取或改写未授权文件。
func (w *Worker) SwitchSession(ctx context.Context, sessionPath string) (string, error) {
	if sessionPath == "" {
		return "", protocol.E("invalid_params", "sessionPath 不能为空")
	}
	clean := filepath.Clean(sessionPath)
	if !filepath.IsAbs(clean) {
		return "", protocol.E("invalid_params", "sessionPath 必须是绝对路径")
	}
	id := strings.TrimSuffix(filepath.Base(clean), ".jsonl")
	if !sessions.ValidID(id) {
		return "", protocol.E("invalid_params", "无法从路径解析会话 ID")
	}
	if _, err := w.store.Find(ctx, id); err != nil {
		return "", err
	}
	if want := w.store.Path(mustFind(ctx, w.store, id)); filepath.Clean(want) != clean {
		return "", protocol.E("forbidden", "sessionPath 不在受管会话目录内")
	}
	raw, err := w.call(ctx, "switch_session", map[string]any{"sessionPath": clean}, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Cancelled bool   `json:"cancelled"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", protocol.E("pi_error", "Pi 返回的切换结果无效")
	}
	if out.Cancelled {
		return "", protocol.E("conflict", "扩展取消了切换会话")
	}
	if out.SessionID == "" {
		state, err := w.stateAfterRebind(ctx)
		if err != nil {
			return "", err
		}
		out.SessionID = state.SessionID
	}
	if err := w.owner.Rebind(w, out.SessionID); err != nil {
		return "", err
	}
	return out.SessionID, nil
}

// Fork 从指定条目分叉出新会话，并重绑定进程表。
func (w *Worker) Fork(ctx context.Context, entryID string) (map[string]any, error) {
	if entryID == "" {
		return nil, protocol.E("invalid_params", "entryId 不能为空")
	}
	raw, err := w.call(ctx, "fork", map[string]any{"entryId": entryID}, true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Text      string `json:"text"`
		Cancelled bool   `json:"cancelled"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的 fork 结果无效")
	}
	if out.Cancelled {
		return nil, protocol.E("conflict", "扩展取消了 fork")
	}
	state, err := w.stateAfterRebind(ctx)
	if err != nil {
		return nil, err
	}
	if err := w.owner.Rebind(w, state.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": state.SessionID, "text": out.Text}, nil
}

// Clone 复制当前分支到新会话，并重绑定进程表。
func (w *Worker) Clone(ctx context.Context) (string, error) {
	raw, err := w.call(ctx, "clone", nil, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", protocol.E("pi_error", "Pi 返回的 clone 结果无效")
	}
	if out.Cancelled {
		return "", protocol.E("conflict", "扩展取消了 clone")
	}
	state, err := w.stateAfterRebind(ctx)
	if err != nil {
		return "", err
	}
	if err := w.owner.Rebind(w, state.SessionID); err != nil {
		return "", err
	}
	return state.SessionID, nil
}

// resetReplay 让补发环失效；会话身份变化后旧序号不再有意义。
func (w *Worker) resetReplay() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq = 0
	w.replay = newReplayRing(w.cfg)
}

func mustFind(ctx context.Context, store *sessions.Store, id string) sessions.Header {
	h, err := store.Find(ctx, id)
	if err != nil {
		return sessions.Header{}
	}
	return h
}
