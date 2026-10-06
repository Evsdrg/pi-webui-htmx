package transport

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/relay"
	"pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/terminal"
)

// TunnelBridge 把云端隧道帧接入本地桥。
// 每个 clientId 对应一个虚拟浏览器连接；空闲超时后回收，
// 避免云端重连在本地留下大量连接状态。
type TunnelBridge struct {
	server *Server
	sender func([]byte) error
	max    int
	idle   time.Duration
	// maxHTTPResponse 是单次 HTTP 转发允许回传的响应体上限。
	maxHTTPResponse int64

	mu      sync.Mutex
	virtual map[string]*virtualConn
	closed  bool
}

// virtualConn 是一条隧道上的逻辑浏览器连接。
//
// 并发结构：handle 只把 session.subscribe/unsubscribe 串行化，其余命令
// （含 terminal.*）走并行分发，所以 subs/terms 两个映射都由 bridge.mu 统一
// 保护，任何访问点都不能裸读裸写（B73 当初只看到 subs 就把问题收了工）。
type virtualConn struct {
	id      string
	bridge  *TunnelBridge
	queue   *outboundQueue
	ctx     context.Context
	cancel  context.CancelFunc
	lastUse time.Time
	// subs 记录每个会话的真实订阅句柄。只存标记不够：
	// 退订时必须 Close 掉底层订阅，否则 worker 的订阅配额会被
	// 反复订阅/退订耗尽（B14）。
	subs  map[string]*runtime.Subscription
	terms map[string]*terminal.Subscription
	// dead 表示 pump 已退出（隧道发送失败）。死连接必须从映射移除，
	// 否则同一 clientId 重连会复用它，响应入队却无人发送（B57）。
	dead atomic.Bool
}

// connHandles 是一批待关闭的句柄。
type connHandles struct {
	subs  []*runtime.Subscription
	terms []*terminal.Subscription
}

// NewTunnelBridge 构造隧道接入层。
func NewTunnelBridge(server *Server, sender func([]byte) error, max int, idle time.Duration) *TunnelBridge {
	if max <= 0 {
		max = 8
	}
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	t := &TunnelBridge{
		server: server,
		sender: sender,
		max:    max,
		idle:   idle,
		// 单次 HTTP 转发的响应上限：按桥实际的内容上限派生，不是写死的常量。
		maxHTTPResponse: tunnelResponseBudget(server.files),
		virtual:         map[string]*virtualConn{},
	}
	go t.reap()
	return t
}

// ResponseBudget 返回单次 HTTP 转发允许回传的响应体上限。
// 装配方用它把隧道发送队列配到足以承载一帧最大响应——HTTPEnvelope.Body
// 是 []byte，JSON 序列化走 base64（约 1.33×）再加信封开销；队列小于单帧
// 时 Send 会直接拒绝，大响应被静默丢弃、云端挂到 504（本地直连却正常）。
func (t *TunnelBridge) ResponseBudget() int64 {
	return t.maxHTTPResponse
}

// SetSender 设置隧道发送函数；必须在启动隧道前调用。
func (t *TunnelBridge) SetSender(sender func([]byte) error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sender = sender
}

// Close 关闭全部虚拟连接。
func (t *TunnelBridge) Close() {
	t.mu.Lock()
	t.closed = true
	conns := make([]*virtualConn, 0, len(t.virtual))
	for _, c := range t.virtual {
		conns = append(conns, c)
	}
	t.virtual = map[string]*virtualConn{}
	t.mu.Unlock()
	for _, c := range conns {
		c.cancel()
	}
}

// Stats 返回虚拟连接规模。
func (t *TunnelBridge) Stats() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{"virtualConnections": len(t.virtual), "max": t.max, "idleSeconds": int(t.idle.Seconds())}
}

func (t *TunnelBridge) reap() {
	interval := t.idle / 4
	if interval < time.Second {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range tick.C {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		stale := make([]*virtualConn, 0, 4)
		for _, c := range t.virtual {
			if time.Since(c.lastUse) >= t.idle {
				stale = append(stale, c)
			}
		}
		for _, c := range stale {
			delete(t.virtual, c.id)
		}
		t.mu.Unlock()
		for _, c := range stale {
			c.cancel()
		}
	}
}

// HandleFrame 实现 tunnel.Handler：处理一条来自云端的浏览器帧。
func (t *TunnelBridge) HandleFrame(ctx context.Context, frame []byte) bool {
	rf, ok := relay.Unwrap(frame)
	if !ok {
		return false
	}
	// HTTP 转发帧（B54）：不走虚拟 WS 连接，直接投给桥自己的 HTTP handler。
	// 它没有 From——HTTP 请求本身不绑定标签页，响应按 ID 配对。
	if rf.HTTP != nil {
		return t.handleTunnelHTTP(ctx, *rf.HTTP)
	}
	if rf.From == "" {
		return false
	}
	conn := t.acquire(rf.From)
	if conn == nil {
		return false
	}
	conn.touch()
	conn.handle(ctx, rf.Data)
	return true
}

// acquire 取到（或创建）某个 clientId 的虚拟连接。
//
// 锁只圈住映射操作，摘下来的句柄一律在解锁之后关闭。历史写法是
// defer 解锁、又在锁内调 releaseAll，而 releaseAll 自己还要拿同一把
// 不可重入的 mutex——一旦撞上「dead 已置位、尚未从映射摘除」的窗口，
// 整个 TunnelBridge（含 reap/Close/其他连接）会被永久锁死。
func (t *TunnelBridge) acquire(id string) *virtualConn {
	if len(id) > 64 {
		id = id[:64]
	}
	var stale []connHandles
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	if c, ok := t.virtual[id]; ok {
		if !c.dead.Load() {
			t.mu.Unlock()
			return c
		}
		// 死连接的 pump 已退出，不能再复用：同一 clientId 重连时
		// 响应会静静堆在队列里。换成新连接。
		delete(t.virtual, id)
		c.cancel()
		stale = append(stale, c.takeLocked())
	}
	if len(t.virtual) >= t.max {
		// 达到上限时淘汰最久未用的一个，保证状态有界。
		var oldest *virtualConn
		for _, c := range t.virtual {
			if oldest == nil || c.lastUse.Before(oldest.lastUse) {
				oldest = c
			}
		}
		if oldest != nil {
			delete(t.virtual, oldest.id)
			oldest.cancel()
			stale = append(stale, oldest.takeLocked())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &virtualConn{
		id: id, bridge: t, queue: newOutboundQueue(64),
		ctx: ctx, cancel: cancel, lastUse: time.Now(),
		subs: map[string]*runtime.Subscription{}, terms: map[string]*terminal.Subscription{},
	}
	t.virtual[id] = c
	t.mu.Unlock()
	go c.pump()
	closeHandles(stale)
	return c
}

func (c *virtualConn) touch() {
	c.bridge.mu.Lock()
	c.lastUse = time.Now()
	c.bridge.mu.Unlock()
}

// pump 把响应帧经隧道发回对应浏览器。
func (c *virtualConn) pump() {
	if c.bridge.sender == nil {
		c.markDead()
		return
	}
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.queue.frames:
			c.queue.release(int64(len(b)))
			wrapped := relay.RouteTo(c.id, b)
			if wrapped == nil {
				continue
			}
			if err := c.bridge.sender(wrapped); err != nil {
				// 发送失败说明隧道已断：这条虚拟连接不能再用于回包。
				// 必须标记死亡并从映射摘除，否则同 clientId 重连会复用它。
				c.markDead()
				return
			}
		}
	}
}

// markDead 标记连接已死并把它从桥的映射里摘除，
// 同时释放已占用的订阅与终端，避免资源滞留到空闲回收才生效。
func (c *virtualConn) markDead() {
	if c.dead.Swap(true) {
		return
	}
	c.bridge.mu.Lock()
	if cur, ok := c.bridge.virtual[c.id]; ok && cur == c {
		delete(c.bridge.virtual, c.id)
	}
	c.bridge.mu.Unlock()
	c.cancel()
	c.releaseAll()
}

// releaseAll 关闭该虚拟连接持有的全部订阅与终端。
func (c *virtualConn) releaseAll() {
	c.bridge.mu.Lock()
	handles := c.takeLocked()
	c.bridge.mu.Unlock()
	closeHandles([]connHandles{handles})
}

// takeLocked 摘出该连接持有的全部句柄并清空映射，调用方必须已持有 bridge.mu。
//
// 摘取与关闭必须分成两步：Close 会走回桥的其他路径，持锁关闭容易形成
// 锁序问题；而且 releaseAll 的调用方 acquire 本身就持着锁。
func (c *virtualConn) takeLocked() connHandles {
	h := connHandles{
		subs:  make([]*runtime.Subscription, 0, len(c.subs)),
		terms: make([]*terminal.Subscription, 0, len(c.terms)),
	}
	for _, sub := range c.subs {
		h.subs = append(h.subs, sub)
	}
	c.subs = map[string]*runtime.Subscription{}
	for _, sub := range c.terms {
		h.terms = append(h.terms, sub)
	}
	c.terms = map[string]*terminal.Subscription{}
	return h
}

// closeHandles 在锁外关闭句柄。
func closeHandles(list []connHandles) {
	for _, h := range list {
		for _, sub := range h.subs {
			sub.Close()
		}
		for _, sub := range h.terms {
			sub.Close()
		}
	}
}

// handle 处理一条命令，复用 server 的分发逻辑。
func (c *virtualConn) handle(ctx context.Context, raw []byte) {
	var req protocol.Request
	if protocol.Decode(raw, &req) != nil {
		c.reply(protocol.Reply("", nil, protocol.E("invalid_request", "需要一条 JSON 命令")))
		return
	}
	// 与 WebSocket 入口共用同一套准入：去重、指纹、并发预算不能各写一份。
	ok, urgent := c.bridge.server.admit(req, c.reply)
	if !ok {
		return
	}
	release := make(chan struct{}, 1)
	if !urgent {
		select {
		case release <- struct{}{}:
		default:
			c.bridge.server.claims.finish(req.RequestID)
			c.reply(protocol.Reply(req.RequestID, nil, protocol.E("busy", "该连接的在途命令数已达上限")))
			return
		}
		// 隧道命令同样受桥的全局并发上限约束，不能绕过资源限额（B15）。
		select {
		case c.bridge.server.operations <- struct{}{}:
		default:
			c.bridge.server.claims.finish(req.RequestID)
			c.reply(protocol.Reply(req.RequestID, nil, protocol.E("busy", "桥的在途命令数已达上限")))
			return
		}
	}
	// 连接资源命令按入站顺序完成：若退订抢在尚未登记的订阅前执行，
	// 新订阅会永久留在 worker 上。普通命令仍并行，取消/停止仍可插队。
	if req.Method == "session.subscribe" || req.Method == "session.unsubscribe" {
		if !urgent {
			<-release
			defer func() { <-c.bridge.server.operations }()
		}
		c.bridge.server.runCommand(c, req)
		return
	}
	go func() {
		if !urgent {
			<-release
			defer func() { <-c.bridge.server.operations }()
		}
		c.bridge.server.runCommand(c, req)
	}()
}

// dispatch 实现 connSink：订阅类命令走虚拟连接自己的实现，其余共用。
func (c *virtualConn) dispatch(ctx context.Context, r protocol.Request) (any, error) {
	switch r.Method {
	case "session.subscribe":
		return c.subscribe(r)
	case "session.unsubscribe":
		return c.unsubscribe(r)
	default:
		return c.bridge.server.dispatchCommon(ctx, r, c)
	}
}

func (c *virtualConn) subscribe(r protocol.Request) (any, error) {
	return c.bridge.server.subscribeWithReplay(c, r)
}

func (c *virtualConn) unsubscribe(r protocol.Request) (any, error) {
	if err := protocol.Decode(r.Params, &struct{}{}); err != nil {
		return nil, err
	}
	c.dropSubscription(r.SessionID)
	return map[string]bool{"subscribed": false}, nil
}

// trackSubscription 实现 connSink。
func (c *virtualConn) trackSubscription(id string, sub *runtime.Subscription) {
	c.bridge.mu.Lock()
	c.subs[id] = sub
	c.bridge.mu.Unlock()
}

// existingSubscription 实现 connSink：取回旧订阅供调用方关闭。
func (c *virtualConn) existingSubscription(id string) *runtime.Subscription {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	return c.subs[id]
}

// dropSubscription 关闭并移除某个会话的订阅。
// 只从 map 删除不够：底层订阅不关，worker 的订阅配额不会被释放（B14）。
func (c *virtualConn) dropSubscription(sessionID string) {
	c.bridge.mu.Lock()
	sub, ok := c.subs[sessionID]
	if ok {
		delete(c.subs, sessionID)
	}
	c.bridge.mu.Unlock()
	if ok {
		sub.Close()
	}
}

// send 实现 connSink。
func (c *virtualConn) send(m protocol.Message) bool {
	b, err := json.Marshal(m)
	if err != nil {
		c.cancel()
		return false
	}
	if len(b) > outboundFrameLimit {
		alt, ok := frameFallback(m)
		if !ok {
			c.cancel()
			return false
		}
		b = alt
	}
	ctx, cancel := context.WithTimeout(c.ctx, outboundWait)
	defer cancel()
	if !c.queue.enqueue(ctx, b) {
		c.cancel()
		return false
	}
	return true
}

// sendRaw 发送已序列化的补发帧，按整批截止时间等待队列排空。
func (c *virtualConn) sendRaw(ctx context.Context, b []byte) bool {
	return c.queue.enqueue(ctx, b)
}

func (c *virtualConn) reply(m protocol.Message) { c.send(m) }

// connContext 实现 connSink。
func (c *virtualConn) connContext() context.Context { return c.ctx }

// trackTerminal 实现 connSink。
func (c *virtualConn) trackTerminal(id string, sub *terminal.Subscription) {
	c.bridge.mu.Lock()
	c.terms[id] = sub
	c.bridge.mu.Unlock()
}

// dropTerminal 实现 connSink。
// 与 dropSubscription 同形：锁内摘除，锁外关闭。
func (c *virtualConn) dropTerminal(id string) {
	c.bridge.mu.Lock()
	sub, ok := c.terms[id]
	if ok {
		delete(c.terms, id)
	}
	c.bridge.mu.Unlock()
	if ok {
		sub.Close()
	}
}
