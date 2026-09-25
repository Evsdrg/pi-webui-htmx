package transport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"pi-bridge-go/internal/observe"
	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/workspace"
)

// cookieName 是浏览器换取会话 Cookie 后使用的凭据名。
const cookieName = "pi_bridge_session"

// Server 是 HTTP 与 WebSocket 入口，只做接入、鉴权与限额。
type Server struct {
	manager      *run.Manager
	store        *sessions.Store
	terminals    *terminal.Manager
	files        *workspace.Files
	receipts     *storage.Receipts
	metrics      *observe.Metrics
	token, host  string
	connections  chan struct{}
	operations   chan struct{}
	tunnelBridge *TunnelBridge
}

// New 构造入口；token 至少 32 字符，host 为监听地址上的主机名。
// New 构造入口；token 至少 32 字符，host 为监听地址上的主机名。
func New(manager *run.Manager, store *sessions.Store, terminals *terminal.Manager, files *workspace.Files, receipts *storage.Receipts, metrics *observe.Metrics, token, host string) *Server {
	return &Server{
		manager:     manager,
		store:       store,
		terminals:   terminals,
		files:       files,
		receipts:    receipts,
		metrics:     metrics,
		token:       token,
		host:        host,
		connections: make(chan struct{}, 8),
		operations:  make(chan struct{}, 16),
	}
}

// SetTunnelBridge 注入隧道接入层，使云端帧能复用同一套命令分发。
func (s *Server) SetTunnelBridge(b *TunnelBridge) { s.tunnelBridge = b }

// bearer 校验 Bearer token；只接受请求头，不接收 URL 查询参数。
func (s *Server) bearer(r *http.Request) bool {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if value == r.Header.Get("Authorization") {
		return false
	}
	got := sha256.Sum256([]byte(value))
	want := sha256.Sum256([]byte(s.token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// signature 用 HMAC 生成带过期时间的 Cookie 值，避免依赖服务端会话存储。
func (s *Server) signature(exp string) string {
	m := hmac.New(sha256.New, []byte(s.token))
	m.Write([]byte(exp + "|" + s.host))
	return hex.EncodeToString(m.Sum(nil))
}

// authorized 同时接受 Bearer token 与未过期的会话 Cookie。
func (s *Server) authorized(r *http.Request) bool {
	if s.bearer(r) {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return false
	}
	return hmac.Equal([]byte(parts[1]), []byte(s.signature(parts[0])))
}

// ServeHTTP 统一做 Host、Origin、鉴权与限额检查，再分发到具体端点。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != s.host {
		writeError(w, http.StatusForbidden, protocol.E("host_denied", "Host 不在预期范围内"))
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != scheme+"://"+s.host {
		writeError(w, http.StatusForbidden, protocol.E("origin_denied", "未启用跨源访问"))
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth" {
		if !s.bearer(r) {
			writeError(w, 401, protocol.E("unauthorized", "需要 Bearer token"))
			return
		}
		expires := time.Now().Add(8 * time.Hour)
		exp := strconv.FormatInt(expires.Unix(), 10)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: exp + "." + s.signature(exp), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Path: "/api/v1/", Expires: expires, MaxAge: 8 * 60 * 60})
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if !s.authorized(r) {
		s.metrics.AuthFailure()
		writeError(w, 401, protocol.E("unauthorized", "需要身份验证"))
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, protocol.E("invalid_request", "请求方法不被允许"))
		return
	}
	switch r.URL.Path {
	case "/api/v1/capabilities":
		writeJSON(w, 200, map[string]any{"version": 1, "phase": "A", "piBaseline": "0.85.1", "methods": []string{"session.start", "session.state", "session.prompt", "session.abort", "session.stop", "session.subscribe", "session.unsubscribe", "worker.list"}, "replay": true, "persistentDedup": false, "extensionDialogs": "interactive", "relay": false, "history": "v3-disk-branch", "limits": map[string]int{"connections": 8, "inFlightOperations": 16, "wsRequestBytes": 1 << 20, "wsResponseBytes": 512 << 10, "connectionQueueBytes": 1 << 20, "requestIdsPerConnection": 1024, "replayItems": 256, "replayBytes": 1 << 20}})
	case "/api/v1/sessions":
		limit, err := number(r, "limit", 50)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		offset, err := number(r, "offset", 0)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		list, err := s.store.List(r.Context(), offset, limit)
		respond(w, list, err)
	case "/api/v1/metrics":
		out := map[string]any{"metrics": s.metrics.Snapshot(), "sessions": s.store.Index().Stats(), "receipts": s.receipts.Stats(), "workers": s.manager.List(), "terminals": s.terminals.List()}
		if s.tunnelBridge != nil {
			out["tunnel"] = s.tunnelBridge.Stats()
		}
		writeJSON(w, 200, out)
	case "/api/v1/ws":
		s.serveWS(w, r)
	default:
		prefix := "/api/v1/sessions/"
		suffix := "/history"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
			limit, err := number(r, "limit", 50)
			if err != nil {
				writeError(w, 400, err)
				return
			}
			s.metrics.HistoryRequest()
			page, err := s.store.History(r.Context(), id, r.URL.Query().Get("leafId"), r.URL.Query().Get("before"), limit)
			respond(w, page, err)
			return
		}
		writeError(w, 404, protocol.E("not_found", "接口不存在"))
	}
}

// number 解析分页用数字参数。
func number(r *http.Request, key string, fallback int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, protocol.E("invalid_params", "数字查询参数无效")
	}
	return n, nil
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 把内部错误转成协议错误响应。
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, protocol.Reply("", nil, err))
}

// respond 按错误码映射 HTTP 状态；未识别的错误一律按 500 处理。
func respond(w http.ResponseWriter, data any, err error) {
	if err == nil {
		writeJSON(w, 200, data)
		return
	}
	status := 500
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "invalid_params", "invalid_history":
			status = 400
		case "forbidden":
			status = 403
		case "not_found":
			status = 404
		case "conflict", "busy":
			status = 409
		case "unsupported_version":
			status = 422
		case "limit_exceeded":
			status = 413
		}
	}
	writeError(w, status, err)
}

// connection 是单条 WebSocket 连接的发送队列、订阅与命令信号量。
type connection struct {
	server         *Server
	ws             *websocket.Conn
	ctx            context.Context
	cancel         context.CancelFunc
	out            chan []byte
	queued         atomic.Int64
	mu             sync.Mutex
	subs           map[string]*run.Subscription
	termSubs       map[string]*terminal.Subscription
	normal, urgent chan struct{}
}

// trackTerminal 登记终端订阅，连接关闭时统一解除。
func (c *connection) trackTerminal(id string, sub *terminal.Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.termSubs == nil {
		c.termSubs = map[string]*terminal.Subscription{}
	}
	if old, ok := c.termSubs[id]; ok {
		old.Close()
	}
	c.termSubs[id] = sub
}

// dropTerminal 解除并移除终端订阅。
func (c *connection) dropTerminal(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sub, ok := c.termSubs[id]; ok {
		sub.Close()
		delete(c.termSubs, id)
	}
}

// send 把消息放入发送队列；单条或队列总量超限即判定该连接异常并关闭，
// 绝不因为某个慢客户端拖住 Pi 输出或让队列无界增长。
// sendRaw 发送已序列化的帧，用于补发。
func (c *connection) sendRaw(b []byte) bool {
	if len(b) == 0 || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	if c.queued.Add(int64(len(b))) > 1<<20 {
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		c.queued.Add(-int64(len(b)))
		return false
	default:
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
}

func (c *connection) send(m protocol.Message) bool {
	b, err := json.Marshal(m)
	if err != nil || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	if c.queued.Add(int64(len(b))) > 1<<20 {
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		c.queued.Add(-int64(len(b)))
		return false
	default:
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
}

// writer 是连接内唯一的写协程，保证 WebSocket 写入串行化。
func (c *connection) writer() {
	defer c.cancel()
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			c.queued.Add(-int64(len(b)))
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// serveWS 升级连接，随后串行读取命令、异步执行，读循环永不被命令阻塞。
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	select {
	case s.connections <- struct{}{}:
		defer func() { <-s.connections }()
	default:
		writeError(w, 429, protocol.E("limit_exceeded", "连接数量已达上限"))
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(s.manager.Context())
	defer cancel()
	c := &connection{server: s, ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, 32), subs: map[string]*run.Subscription{}, termSubs: map[string]*terminal.Subscription{}, normal: make(chan struct{}, 8), urgent: make(chan struct{}, 2)}
	go c.writer()
	defer func() {
		c.mu.Lock()
		for id, sub := range c.subs {
			delete(c.subs, id)
			sub.Close()
		}
		for id, sub := range c.termSubs {
			delete(c.termSubs, id)
			sub.Close()
		}
		c.mu.Unlock()
	}()
	seen := map[string]bool{}
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var req protocol.Request
		if typ != websocket.MessageText || protocol.Decode(b, &req) != nil {
			c.send(protocol.Reply("", nil, protocol.E("invalid_request", "需要一条 JSON 命令")))
			continue
		}
		if req.Version != 1 {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("unsupported_version", "仅支持版本 1")))
			continue
		}
		if req.Kind != "command" || req.RequestID == "" || len(req.RequestID) > 128 {
			c.send(protocol.Reply("", nil, protocol.E("invalid_request", "命令必须带长度受限的 requestId")))
			continue
		}
		// 跨重启去重：同一 requestId 已执行过就直接回放结论，绝不重新执行。
		if rec, ok := s.receipts.Lookup(req.RequestID); ok && rec.Outcome != storage.OutcomeRejected {
			c.send(protocol.Reply(req.RequestID, map[string]any{
				"duplicate": true,
				"outcome":   string(rec.Outcome),
				"method":    rec.Method,
				"at":        rec.At.UTC().Format(time.RFC3339Nano),
			}, nil))
			continue
		}
		if seen[req.RequestID] {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("conflict", "requestId 已被使用，有副作用的命令请勿重试")))
			continue
		}
		if len(seen) >= 1024 {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("limit_exceeded", "请用新的 requestId 重新连接")))
			return
		}
		seen[req.RequestID] = true
		urgent := req.Method == "session.abort" || req.Method == "session.stop"
		sem := c.normal
		if urgent {
			sem = c.urgent
		}
		select {
		case sem <- struct{}{}:
		default:
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("busy", "该连接的在途命令数已达上限")))
			continue
		}
		if !urgent {
			select {
			case s.operations <- struct{}{}:
			default:
				<-sem
				c.send(protocol.Reply(req.RequestID, nil, protocol.E("busy", "桥的在途命令数已达上限")))
				continue
			}
		}
		go func(req protocol.Request, sem chan struct{}, urgent bool) {
			defer func() {
				<-sem
				if !urgent {
					<-s.operations
				}
			}()
			// 命令寿命有意长于浏览器连接：断开只停止等待，不取消已接受的任务。
			opctx, stop := context.WithTimeout(s.manager.Context(), s.manager.Timeout())
			defer stop()
			s.metrics.CommandStarted(req.Method)
			data, err := c.dispatch(opctx, req)
			if err != nil {
				s.metrics.CommandFailed(req.Method, errorCodeOf(err))
			}
			c.send(protocol.Reply(req.RequestID, data, err))
			s.recordReceipt(req, err)
		}(req, sem, urgent)
	}
}

// errorCodeOf 取协议错误码，未知错误归一化。
func errorCodeOf(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return "internal"
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

// recordReceipt 落一条命令回执，供跨重启去重与对账。
// 写失败不影响命令结果。
func (s *Server) recordReceipt(req protocol.Request, err error) {
	if s.receipts == nil {
		return
	}
	outcome := storage.OutcomeOK
	if err != nil {
		code := errorCodeOf(err)
		if _, skip := notExecutedCodes[code]; skip {
			outcome = storage.OutcomeRejected
		} else if code == "outcome_unknown" {
			outcome = storage.OutcomeUnknown
		} else {
			outcome = storage.OutcomeError
		}
	}
	_ = s.receipts.Record(storage.Receipt{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Method:    req.Method,
		Outcome:   outcome,
	})
}

// connSink 是连接相关的少量能力：生命周期上下文、发送帧、登记终端订阅。
// WebSocket 连接与隧道虚拟连接各自实现它，从而共用同一份命令分发。
type connSink interface {
	connContext() context.Context
	send(m protocol.Message) bool
	trackTerminal(id string, sub *terminal.Subscription)
	dropTerminal(id string)
}

// dispatchCommon 执行除订阅以外的命令。
// WebSocket 连接与隧道虚拟连接共用这一份实现，避免两处逻辑漂移。
func (s *Server) dispatchCommon(ctx context.Context, r protocol.Request, sink connSink) (any, error) {
	empty := func() error { return protocol.Decode(r.Params, &struct{}{}) }
	switch r.Method {
	case "worker.list":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.manager.List(), nil
	case "session.start":
		var p struct {
			Cwd string `json:"cwd"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		w, err := s.manager.Start(ctx, r.SessionID, p.Cwd)
		if err != nil {
			return nil, err
		}
		return w.Info(), nil
	}
	w, err := s.manager.Get(r.SessionID)
	if err != nil {
		return nil, err
	}
	switch r.Method {
	case "session.state":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.State(ctx)
	case "session.models":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Models(ctx)
	case "session.set_model":
		var p struct {
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.SetModel(ctx, p.Provider, p.ModelID)
	case "session.cycle_model":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.CycleModel(ctx)
	case "session.thinking_levels":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.ThinkingLevels(ctx)
	case "session.set_thinking":
		var p struct {
			Level string `json:"level"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetThinkingLevel(ctx, p.Level); err != nil {
			return nil, err
		}
		return map[string]any{"level": p.Level}, nil
	case "session.cycle_thinking":
		if err := empty(); err != nil {
			return nil, err
		}
		level, err := w.CycleThinkingLevel(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"level": level}, nil
	case "session.set_queue_mode":
		var p struct {
			Kind string `json:"kind"`
			Mode string `json:"mode"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetQueueMode(ctx, p.Kind, p.Mode); err != nil {
			return nil, err
		}
		return map[string]any{"kind": p.Kind, "mode": p.Mode}, nil
	case "session.steer":
		var p struct {
			Text string `json:"text"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.Steer(ctx, p.Text); err != nil {
			return nil, err
		}
		return map[string]bool{"queued": true}, nil
	case "session.follow_up":
		var p struct {
			Text string `json:"text"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.FollowUp(ctx, p.Text); err != nil {
			return nil, err
		}
		return map[string]bool{"queued": true}, nil
	case "session.compact":
		var p struct {
			Instructions string `json:"customInstructions"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Compact(ctx, p.Instructions)
	case "session.set_auto_compaction":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoCompaction(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": p.Enabled}, nil
	case "session.set_auto_retry":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoRetry(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": p.Enabled}, nil
	case "session.abort_retry":
		if err := empty(); err != nil {
			return nil, err
		}
		if err := w.AbortRetry(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"aborted": true}, nil
	case "session.stats":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Stats(ctx)
	case "session.set_name":
		var p struct {
			Name string `json:"name"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetName(ctx, p.Name); err != nil {
			return nil, err
		}
		return map[string]bool{"renamed": true}, nil
	case "session.last_assistant":
		if err := empty(); err != nil {
			return nil, err
		}
		text, err := w.LastAssistantText(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	case "session.commands":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Commands(ctx)
	case "session.tree":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Tree(ctx)
	case "session.fork_messages":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.ForkMessages(ctx)
	case "session.entries":
		var p struct {
			Since string `json:"since"`
			Limit int    `json:"limit"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		return w.Entries(ctx, p.Since, p.Limit)
	case "session.new":
		var p struct {
			ParentSession string `json:"parentSession"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.NewSession(ctx, p.ParentSession)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "session.switch":
		var p struct {
			SessionPath string `json:"sessionPath"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.SwitchSession(ctx, p.SessionPath)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "session.fork":
		var p struct {
			EntryID string `json:"entryId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Fork(ctx, p.EntryID)
	case "session.clone":
		if err := empty(); err != nil {
			return nil, err
		}
		id, err := w.Clone(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "session.bash":
		var p struct {
			Command            string `json:"command"`
			ExcludeFromContext bool   `json:"excludeFromContext"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		result, err := w.Bash(ctx, r.RequestID, p.Command, p.ExcludeFromContext)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "session.abort_bash":
		if err := empty(); err != nil {
			return nil, err
		}
		if err := w.AbortBash(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"aborted": true}, nil
	case "session.ui_response":
		var p struct {
			ID        string  `json:"id"`
			Value     *string `json:"value"`
			Confirmed *bool   `json:"confirmed"`
			Cancelled bool    `json:"cancelled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.UIResponse(ctx, p.ID, p.Value, p.Confirmed, p.Cancelled); err != nil {
			return nil, err
		}
		return map[string]bool{"answered": true}, nil
	case "session.pending_dialogs":
		if err := empty(); err != nil {
			return nil, err
		}
		return map[string]any{"ids": w.PendingDialogs()}, nil
	case "terminal.open":
		var p struct {
			Cwd   string `json:"cwd"`
			Shell string `json:"shell"`
			Cols  uint16 `json:"cols"`
			Rows  uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Cwd == "" {
			return nil, protocol.E("invalid_params", "cwd 不能为空")
		}
		term, err := s.terminals.Open(p.Cwd, p.Shell, p.Cols, p.Rows)
		if err != nil {
			return nil, err
		}
		info := term.Info()
		sub, err := term.Subscribe(64, 1<<20)
		if err != nil {
			_ = term.Close(true)
			return nil, protocol.E("worker_exited", "终端已关闭")
		}
		sink.trackTerminal(term.ID(), sub)
		connCtx := sink.connContext()
		go func() {
			defer sub.Close()
			for {
				chunk, err := sub.Next(connCtx)
				if err != nil {
					if connCtx.Err() == nil {
						sink.send(protocol.Message{Version: 1, Kind: "control", Event: "bridge.terminal_closed", Data: map[string]any{"terminalId": term.ID()}})
					}
					return
				}
				if !sink.send(protocol.Message{Version: 1, Kind: "event", Event: "terminal.output", Data: map[string]any{"terminalId": term.ID(), "data": string(chunk)}}) {
					return
				}
			}
		}()
		return map[string]any{"terminalId": info.ID, "cwd": info.Cwd, "pid": info.PID, "cols": info.Cols, "rows": info.Rows}, nil
	case "terminal.input":
		var p struct {
			TerminalID string `json:"terminalId"`
			Data       string `json:"data"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Write([]byte(p.Data)); err != nil {
			return nil, err
		}
		return map[string]bool{"written": true}, nil
	case "terminal.resize":
		var p struct {
			TerminalID string `json:"terminalId"`
			Cols       uint16 `json:"cols"`
			Rows       uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Resize(p.Cols, p.Rows); err != nil {
			return nil, err
		}
		return map[string]bool{"resized": true}, nil
	case "terminal.close":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		sink.dropTerminal(p.TerminalID)
		if err := s.terminals.CloseTerminal(p.TerminalID); err != nil {
			return nil, err
		}
		return map[string]bool{"closed": true}, nil
	case "terminal.list":
		if err := empty(); err != nil {
			return nil, err
		}
		return map[string]any{"terminals": s.terminals.List()}, nil
	case "files.list":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		entries, truncated, err := s.files.List(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"entries": entries, "truncated": truncated}, nil
	case "files.stat":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.Stat(p.Path)
	case "files.read":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, size, err := s.files.Read(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text, "truncated": truncated, "size": size}, nil
	case "files.roots":
		if err := empty(); err != nil {
			return nil, err
		}
		return map[string]any{"roots": s.files.Roots()}, nil
	case "git.status":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.GitStatus(ctx, p.Path)
	case "git.diff":
		var p struct {
			Path     string `json:"path"`
			Staged   bool   `json:"staged"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.MaxBytes == 0 {
			p.MaxBytes = 512 << 10
		}
		text, truncated, err := s.files.GitDiff(ctx, p.Path, p.Staged, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"diff": text, "truncated": truncated}, nil
	case "session.bash_output":
		var p struct {
			Path     string `json:"path"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, err := w.ReadBashOutput(ctx, p.Path, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text, "truncated": truncated}, nil
	case "session.prompt":
		var p struct {
			Text     string `json:"text"`
			Behavior string `json:"streamingBehavior"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.Prompt(ctx, p.Text, p.Behavior); err != nil {
			return nil, err
		}
		return map[string]bool{"accepted": true}, nil
	case "session.abort":
		if err := empty(); err != nil {
			return nil, err
		}
		q, err := w.Abort(ctx)
		return map[string]any{"clearedQueue": q}, err
	case "session.stop":
		var p struct {
			Force bool `json:"force"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		err := w.Stop(p.Force)
		return map[string]bool{"stopped": err == nil}, err
	default:
		return nil, protocol.E("unsupported_method", "A 阶段未实现该方法")
	}
}

// subscribe 处理事件订阅：先做游标补发，再注册有界队列并持续推送。
func (c *connection) subscribe(ctx context.Context, r protocol.Request) (any, error) {
	var p struct {
		Epoch    string  `json:"epoch"`
		AfterSeq *uint64 `json:"afterSeq"`
	}
	if err := protocol.Decode(r.Params, &p); err != nil {
		return nil, err
	}
	w, err := c.server.manager.Get(r.SessionID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return nil, protocol.E("conflict", "连接已关闭")
	}
	// 带游标订阅时先补发，补不上就明确要求重新同步，不伪造无损恢复。
	if p.Epoch != "" || p.AfterSeq != nil {
		after := uint64(0)
		if p.AfterSeq != nil {
			after = *p.AfterSeq
		}
		items, ok := w.Replay(p.Epoch, after)
		if !ok {
			c.server.metrics.ReplayMiss()
			return nil, protocol.E("resync_required", "事件游标已失效，请重新读取持久历史后再订阅")
		}
		if len(items) > 0 {
			c.server.metrics.ReplayHit()
		}
		for _, item := range items {
			if !c.sendRaw(item.Payload) {
				return nil, protocol.E("conflict", "连接已关闭")
			}
		}
	}
	if old := c.subs[r.SessionID]; old != nil {
		old.Close()
		delete(c.subs, r.SessionID)
	}
	sub, info, err := w.Subscribe()
	if err != nil {
		return nil, err
	}
	c.subs[r.SessionID] = sub
	// 先入队订阅确认，再允许推送协程投递事件，避免确认晚于首批事件。
	c.send(protocol.Reply(r.RequestID, map[string]any{"subscribed": true, "epoch": info.Epoch, "seq": info.Seq, "replay": true}, nil))
	go func() {
		defer sub.Close()
		for {
			m, err := sub.Next(c.ctx)
			if err != nil {
				if c.ctx.Err() == nil {
					c.send(protocol.Message{Version: 1, Kind: "control", SessionID: r.SessionID, Event: "bridge.subscription_closed", Data: map[string]bool{"resyncRequired": true}})
				}
				return
			}
			if !c.send(m) {
				return
			}
		}
	}()
	return noReply{}, nil
}

// unsubscribe 只解除该连接的订阅，不中断任务。
func (c *connection) unsubscribe(r protocol.Request) (any, error) {
	if err := protocol.Decode(r.Params, &struct{}{}); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if sub := c.subs[r.SessionID]; sub != nil {
		sub.Close()
		delete(c.subs, r.SessionID)
	}
	c.mu.Unlock()
	return map[string]bool{"subscribed": false}, nil
}

// dispatch 执行一条命令；ctx 只约束等待，不把浏览器断开当作取消任务。
func (c *connection) dispatch(ctx context.Context, r protocol.Request) (any, error) {
	switch r.Method {
	case "session.subscribe":
		return c.subscribe(ctx, r)
	case "session.unsubscribe":
		return c.unsubscribe(r)
	default:
		return c.server.dispatchCommon(ctx, r, c)
	}
}

// connContext 实现 connSink。
func (c *connection) connContext() context.Context { return c.ctx }

// noReply 表示该命令已自行发送响应，无需框架再补一条。
// 当前用于订阅：先发确认，再由推送协程持续发送事件。
type noReply struct{}
