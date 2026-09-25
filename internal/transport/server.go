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
	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
)

// cookieName 是浏览器换取会话 Cookie 后使用的凭据名。
const cookieName = "pi_bridge_session"

// Server 是 HTTP 与 WebSocket 入口，只做接入、鉴权与限额。
type Server struct {
	manager     *run.Manager
	store       *sessions.Store
	token, host string
	connections chan struct{}
	operations  chan struct{}
}

// New 构造入口；token 至少 32 字符，host 为监听地址上的主机名。
func New(manager *run.Manager, store *sessions.Store, token, host string) *Server {
	return &Server{manager: manager, store: store, token: token, host: host, connections: make(chan struct{}, 8), operations: make(chan struct{}, 16)}
}

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
		writeError(w, 401, protocol.E("unauthorized", "需要身份验证"))
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, protocol.E("invalid_request", "请求方法不被允许"))
		return
	}
	switch r.URL.Path {
	case "/api/v1/capabilities":
		writeJSON(w, 200, map[string]any{"version": 1, "phase": "A", "piBaseline": "0.85.1", "methods": []string{"session.start", "session.state", "session.prompt", "session.abort", "session.stop", "session.subscribe", "session.unsubscribe", "worker.list"}, "replay": false, "persistentDedup": false, "extensionDialogs": "cancelled", "relay": false, "history": "v3-disk-branch", "limits": map[string]int{"connections": 8, "inFlightOperations": 16, "wsRequestBytes": 1 << 20, "wsResponseBytes": 512 << 10, "connectionQueueBytes": 1 << 20, "requestIdsPerConnection": 1024}})
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
	normal, urgent chan struct{}
}

// send 把消息放入发送队列；单条或队列总量超限即判定该连接异常并关闭，
// 绝不因为某个慢客户端拖住 Pi 输出或让队列无界增长。
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
	c := &connection{server: s, ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, 32), subs: map[string]*run.Subscription{}, normal: make(chan struct{}, 8), urgent: make(chan struct{}, 2)}
	go c.writer()
	defer func() {
		c.mu.Lock()
		for id, sub := range c.subs {
			delete(c.subs, id)
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
			data, err := c.dispatch(opctx, req)
			c.send(protocol.Reply(req.RequestID, data, err))
		}(req, sem, urgent)
	}
}

// dispatch 执行一条命令。ctx 只约束等待，不把浏览器断开当作取消任务。
func (c *connection) dispatch(ctx context.Context, r protocol.Request) (any, error) {
	empty := func() error { return protocol.Decode(r.Params, &struct{}{}) }
	switch r.Method {
	case "worker.list":
		if err := empty(); err != nil {
			return nil, err
		}
		return c.server.manager.List(), nil
	case "session.start":
		var p struct {
			Cwd string `json:"cwd"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		w, err := c.server.manager.Start(ctx, r.SessionID, p.Cwd)
		if err != nil {
			return nil, err
		}
		return w.Info(), nil
	}
	w, err := c.server.manager.Get(r.SessionID)
	if err != nil {
		return nil, err
	}
	switch r.Method {
	case "session.state":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.State(ctx)
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
	case "session.unsubscribe":
		if err := empty(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if sub := c.subs[r.SessionID]; sub != nil {
			sub.Close()
			delete(c.subs, r.SessionID)
		}
		c.mu.Unlock()
		return map[string]bool{"subscribed": false}, nil
	case "session.subscribe":
		var p struct {
			Epoch    string  `json:"epoch"`
			AfterSeq *uint64 `json:"afterSeq"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Epoch != "" || p.AfterSeq != nil {
			return nil, protocol.E("resync_required", "A 阶段不支持事件补发，请重新读取持久历史后再订阅")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ctx.Err() != nil {
			return nil, protocol.E("conflict", "连接已关闭")
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
		c.send(protocol.Reply(r.RequestID, map[string]any{"subscribed": true, "epoch": info.Epoch, "seq": info.Seq, "replay": false}, nil))
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
	default:
		return nil, protocol.E("unsupported_method", "A 阶段未实现该方法")
	}
}

// noReply 表示该命令已自行发送响应，无需框架再补一条。
// 当前用于订阅：先发确认，再由推送协程持续发送事件。
type noReply struct{}
