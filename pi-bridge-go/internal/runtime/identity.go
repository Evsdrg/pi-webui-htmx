package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	// 已停止（或正在停止）的 worker 不能再登记：退出清理按「当前映射」删除，
	// 若在那之后又插入新键就再也没有删除者——死 worker 会永久留在进程表、
	// 占住配额，Start 还会把它当运行中复用。
	w.mu.Lock()
	stopped := w.closing
	old := w.id
	w.mu.Unlock()
	if stopped {
		return protocol.E("worker_exited", "工作进程已停止，无法重绑定会话身份")
	}
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
	// 身份变更与 session.start 串行：CheckRebindTarget 只查询、不预留，
	// 与并发的 Start 交错时会出现「Pi 已切到目标、Rebind 才发现冲突」的双写。
	w.owner.startMu.Lock()
	defer w.owner.startMu.Unlock()
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
	// 与 Start/其它身份命令串行，关闭 CheckRebindTarget 的 TOCTOU 窗口。
	w.owner.startMu.Lock()
	defer w.owner.startMu.Unlock()
	if sessionPath == "" {
		return "", protocol.E("invalid_params", "sessionPath 不能为空")
	}
	clean := filepath.Clean(sessionPath)
	if !filepath.IsAbs(clean) {
		return "", protocol.E("invalid_params", "sessionPath 必须是绝对路径")
	}
	// Pi 的标准命名是 timestamp_ID.jsonl，直接取 basename 会带上时间戳前缀，
	// 之后按 ID 查索引必然失败（B09）。取不到再退回整个 basename，
	// 兼容桥自己写的 ID.jsonl。
	id := sessionIDFromFileName(filepath.Base(clean))
	if !sessions.ValidID(id) {
		return "", protocol.E("invalid_params", "无法从路径解析会话 ID")
	}
	if _, err := w.store.Find(ctx, id); err != nil {
		return "", err
	}
	if want := w.store.Path(mustFind(ctx, w.store, id)); filepath.Clean(want) != clean {
		return "", protocol.E("forbidden", "sessionPath 不在受管会话目录内")
	}
	// 必须在让 Pi 切换之前检查目标冲突：Rebind 是在切换之后才调的，
	// 若目标会话已有活跃 worker，冲突被发现时 Pi 已经切换，
	// 旧键下的 worker 仍可 Prompt，形成双写（B64）。
	if err := w.owner.CheckRebindTarget(w, id); err != nil {
		return "", err
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
func (w *Worker) Fork(ctx context.Context, entryID string) (ForkReply, error) {
	w.owner.startMu.Lock()
	defer w.owner.startMu.Unlock()
	if entryID == "" {
		return ForkReply{}, protocol.E("invalid_params", "entryId 不能为空")
	}
	raw, err := w.call(ctx, "fork", map[string]any{"entryId": entryID}, true)
	if err != nil {
		return ForkReply{}, err
	}
	var out struct {
		Text      string `json:"text"`
		Cancelled bool   `json:"cancelled"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return ForkReply{}, protocol.E("pi_error", "Pi 返回的 fork 结果无效")
	}
	if out.Cancelled {
		return ForkReply{}, protocol.E("conflict", "扩展取消了 fork")
	}
	state, err := w.stateAfterRebind(ctx)
	if err != nil {
		return ForkReply{}, err
	}
	if err := w.owner.Rebind(w, state.SessionID); err != nil {
		return ForkReply{}, err
	}
	// Pi 对无 assistant 的分支延迟写盘。身份已切换，索引不可用时
	// 保守报告未落盘，不能因只读查询失败让调用方误以为 fork 未执行。
	_, findErr := w.store.Find(ctx, state.SessionID)
	return ForkReply{SessionID: state.SessionID, Text: out.Text, Persisted: findErr == nil}, nil
}

// Clone 复制当前分支到新会话，并重绑定进程表。
func (w *Worker) Clone(ctx context.Context) (string, error) {
	w.owner.startMu.Lock()
	defer w.owner.startMu.Unlock()
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
// 必须同时更换 epoch：只把 seq 归零的话，旧 epoch 配旧 seq 仍会被
// Replay 接受，客户端会读到新会话的事件却以为还在旧游标上（B65）。
func (w *Worker) resetReplay() {
	epoch := make([]byte, 16)
	if _, err := rand.Read(epoch); err != nil {
		// 取不到随机数时也不能沿用旧 epoch：旧游标必须失效。
		epoch = []byte(time.Now().UTC().Format(time.RFC3339Nano))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq = 0
	w.epoch = hex.EncodeToString(epoch)
	// 状态行属于旧进程的快照：换了 epoch 就不再对应当前会话（B36）。
	w.extStatuses = nil
	w.replay = newReplayRing(w.cfg)
}

func mustFind(ctx context.Context, store *sessions.Store, id string) sessions.Header {
	h, err := store.Find(ctx, id)
	if err != nil {
		return sessions.Header{}
	}
	return h
}

// sessionIDFromFileName 从会话文件名解析会话 ID。
// Pi 的标准命名是 <timestamp>_<id>.jsonl（时间戳含 T 与连字符但没有下划线），
// 因此取下划线后最后一段；没有下划线时整个 basename 就是 ID。
func sessionIDFromFileName(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	if at := strings.LastIndexByte(base, '_'); at >= 0 && at+1 < len(base) {
		return base[at+1:]
	}
	return base
}
