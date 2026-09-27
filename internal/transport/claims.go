package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/storage"
)

// claimState 描述一个 requestId 在桥内的当前状态。
type claimState int

const (
	// claimProceed 表示可以派发：该 requestId 尚未被任何连接处理。
	claimProceed claimState = iota
	// claimDuplicate 表示已有终态回执，直接回放结论，绝不重新执行。
	claimDuplicate
	// claimPending 表示同一 requestId 正在另一条连接上执行。
	claimPending
	// claimConflict 表示同一 requestId 被用于不同内容的命令。
	claimConflict
)

// claim 是桥级的命令登记，跨连接共享。
type claim struct {
	fingerprint string
	pending     bool
	at          time.Time
}

// claims 按 requestId 跟踪命令。它是 B04 的修复核心：
// 以前去重状态只在单条连接内，两个连接可以同时通过检查并各自执行
// 同一个有副作用的 requestId。
type claims struct {
	mu      sync.Mutex
	byID    map[string]claim
	order   []string
	maxHold int
}

func newClaims(maxHold int) *claims {
	if maxHold <= 0 {
		maxHold = 1024
	}
	return &claims{byID: map[string]claim{}, maxHold: maxHold}
}

// begin 原子地登记一个 requestId。
// 已有终态回执时返回 claimDuplicate 并带上该回执，调用方不得执行。
func (c *claims) begin(requestID, fingerprint string, existing *storage.Receipt) (claimState, *storage.Receipt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.byID[requestID]; ok {
		if cur.fingerprint != fingerprint {
			return claimConflict, nil
		}
		if cur.pending {
			return claimPending, nil
		}
	}
	c.byID[requestID] = claim{fingerprint: fingerprint, pending: true, at: time.Now()}
	c.order = append(c.order, requestID)
	c.evictLocked()
	if existing != nil {
		return claimDuplicate, existing
	}
	return claimProceed, nil
}

// evictLocked 淘汰已完成的旧登记；在途登记绝不淘汰，
// 否则重试会变成第二次执行。调用方需持有锁。
func (c *claims) evictLocked() {
	if len(c.order) <= c.maxHold {
		return
	}
	// 从最旧开始找第一个已完成的条目淘汰，保留全部在途登记。
	for i, id := range c.order {
		if len(c.order) <= c.maxHold {
			return
		}
		if cur, ok := c.byID[id]; ok && !cur.pending {
			delete(c.byID, id)
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// finish 把登记标记为已完成。回执此时已落盘，后续同一 requestId
// 会走 claimDuplicate 分支回放结论。
func (c *claims) finish(requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.byID[requestID]; ok {
		cur.pending = false
		cur.at = time.Now()
		c.byID[requestID] = cur
	}
}

// stats 返回在途与已登记数量，供诊断。
func (c *claims) stats() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := 0
	for _, cur := range c.byID {
		if cur.pending {
			pending++
		}
	}
	return map[string]any{"tracked": len(c.byID), "pending": pending, "max": c.maxHold}
}

// requestFingerprint 计算命令指纹：同一 requestId 携带不同内容时必须报 conflict，
// 而不是静默复用旧结论。params 按键排序后散列，保证稳定。
func requestFingerprint(req protocol.Request) string {
	h := sha256.New()
	h.Write([]byte(req.Method))
	h.Write([]byte{0})
	h.Write([]byte(req.SessionID))
	h.Write([]byte{0})
	if len(req.Params) > 0 {
		// params 是原始 JSON，键序不保证；解析后重新序列化，
		// Go 的 map 序列化按键排序，得到稳定形式。
		var decoded any
		if json.Unmarshal(req.Params, &decoded) == nil {
			if canonical, err := json.Marshal(decoded); err == nil {
				h.Write(canonical)
			}
		} else {
			h.Write(req.Params)
		}
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// notExecutedCodes 是「命令从未送达 Pi」的错误码集合。
// 这些失败允许客户端用同一 requestId 重试，因此回执记为 rejected，
// 不会在后续连接里被当成重复执行而挡住合法重试。
var notExecutedCodes = map[string]struct{}{
	"invalid_request": {}, "invalid_params": {}, "unsupported_method": {},
	"unsupported_version": {}, "busy": {}, "limit_exceeded": {},
	"resync_required": {}, "unauthorized": {}, "host_denied": {},
	"origin_denied": {},
}

// isNotExecuted 判断错误是否属于「命令从未送达 Pi」。
func isNotExecuted(err error) bool {
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		return false
	}
	_, ok := notExecutedCodes[pe.Code]
	return ok
}

// outcomeFor 把命令结果映射成回执类别。
func outcomeFor(err error) storage.Outcome {
	if err == nil {
		return storage.OutcomeOK
	}
	if isNotExecuted(err) {
		// 明确没执行：记 rejected，客户端可重试。
		return storage.OutcomeRejected
	}
	return storage.OutcomeError
}

// admit 对一条命令做统一准入：版本与形态校验、跨重启回执、跨连接 claim、
// 指纹冲突。reply 用于把结论回给来源，WebSocket 与隧道虚拟连接各自传入。
// 返回 false 表示调用方必须放弃这条命令（结论已通过 reply 发出）。
//
// 两条入口以前各写一份去重逻辑，容易各自漂移；现在只有这一处事实来源。
func (s *Server) admit(req protocol.Request, reply func(protocol.Message)) (bool, bool) {
	if req.Version != protocol.Version {
		reply(protocol.Reply(req.RequestID, nil, protocol.E("unsupported_version", "仅支持版本 1")))
		return false, false
	}
	if req.Kind != "command" || req.RequestID == "" || len(req.RequestID) > 128 {
		reply(protocol.Reply("", nil, protocol.E("invalid_request", "命令必须带长度受限的 requestId")))
		return false, false
	}
	fingerprint := requestFingerprint(req)
	// 先查持久回执：已得出结论的直接回放，绝不重新执行。
	if rec, ok := s.receipts.Lookup(req.RequestID); ok {
		if rec.Fingerprint != "" && rec.Fingerprint != fingerprint {
			reply(protocol.Reply(req.RequestID, nil, protocol.E("conflict", "requestId 已被用于不同内容的命令")))
			return false, false
		}
		switch rec.Outcome {
		case storage.OutcomeRejected:
			// 明确没执行过：允许用同一 requestId 重试。
		case storage.OutcomePending:
			// 上次桥崩在 intent 之后、结论之前。无法证明命令有没有到达 Pi，
			// 只能回答 unknown 让客户端对账，绝不假装成功。
			reply(protocol.Reply(req.RequestID, nil, protocol.E("outcome_unknown", "命令结果未知：桥在上次执行中中断，请先对账")))
			return false, false
		default:
			reply(protocol.Reply(req.RequestID, map[string]any{
				"duplicate": true,
				"outcome":   string(rec.Outcome),
				"method":    rec.Method,
				"at":        rec.At.UTC().Format(time.RFC3339Nano),
			}, nil))
			return false, false
		}
	}
	// 再原子登记，跨连接生效。
	switch state, _ := s.claims.begin(req.RequestID, fingerprint, nil); state {
	case claimPending:
		reply(protocol.Reply(req.RequestID, nil, protocol.E("busy", "同一 requestId 正在执行，请勿重试")))
		return false, false
	case claimConflict:
		reply(protocol.Reply(req.RequestID, nil, protocol.E("conflict", "requestId 已被用于不同内容的命令")))
		return false, false
	}
	return true, protocol.IsUrgent(req.Method)
}

// runCommand 执行一条已通过准入的命令：有副作用时先写 intent，
// 再派发，最后落终态回执并释放 claim。
// 命令寿命有意长于浏览器连接：断开只停止等待，不取消已接受的任务。
func (s *Server) runCommand(c connSink, req protocol.Request) {
	if protocol.NeedsIntent(req.Method) {
		_ = s.receipts.Record(storage.Receipt{
			RequestID:   req.RequestID,
			SessionID:   req.SessionID,
			Method:      req.Method,
			Outcome:     storage.OutcomePending,
			Fingerprint: requestFingerprint(req),
		})
	}
	ctx, stop := context.WithTimeout(s.manager.Context(), s.manager.Timeout())
	defer stop()
	s.metrics.CommandStarted(req.Method)
	// 必须走连接自己的 dispatch：订阅类命令由各连接自行实现，
	// 直接调 dispatchCommon 会绕过它们。
	data, err := c.dispatch(ctx, req)
	if err != nil {
		s.metrics.CommandFailed(req.Method, errorCodeOf(err))
	}
	c.send(protocol.Reply(req.RequestID, data, err))
	s.recordReceipt(req, err)
	s.claims.finish(req.RequestID)
}
