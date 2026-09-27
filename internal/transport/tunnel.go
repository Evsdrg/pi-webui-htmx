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

	mu      sync.Mutex
	virtual map[string]*virtualConn
	closed  bool
}

// virtualConn 是一条隧道上的逻辑浏览器连接。
type virtualConn struct {
	id      string
	bridge  *TunnelBridge
	out     chan []byte
	queued  atomic.Int64
	ctx     context.Context
	cancel  context.CancelFunc
	lastUse time.Time
	// subs 记录每个会话的真实订阅句柄。只存标记不够：
	// 退订时必须 Close 掉底层订阅，否则 worker 的订阅配额会被
	// 反复订阅/退订耗尽（B14）。
	subs  map[string]*runtime.Subscription
	terms map[string]*terminal.Subscription
	seen  map[string]bool
	// dead 表示 pump 已退出（隧道发送失败）。死连接必须从映射移除，
	// 否则同一 clientId 重连会复用它，响应入队却无人发送（B57）。
	dead atomic.Bool
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
		server:  server,
		sender:  sender,
		max:     max,
		idle:    idle,
		virtual: map[string]*virtualConn{},
	}
	go t.reap()
	return t
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
	if !ok || rf.From == "" {
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
func (t *TunnelBridge) acquire(id string) *virtualConn {
	if len(id) > 64 {
		id = id[:64]
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	if c, ok := t.virtual[id]; ok {
		// 死连接的 pump 已退出，不能再复用：同一 clientId 重连时
		// 响应会静静堆在队列里。换成新连接。
		if c.dead.Load() {
			delete(t.virtual, id)
			c.cancel()
			c.releaseAll()
		} else {
			return c
		}
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
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &virtualConn{
		id: id, bridge: t, out: make(chan []byte, 64),
		ctx: ctx, cancel: cancel, lastUse: time.Now(),
		subs: map[string]*runtime.Subscription{}, terms: map[string]*terminal.Subscription{},
		seen: map[string]bool{},
	}
	t.virtual[id] = c
	go c.pump()
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
		case b := <-c.out:
			c.queued.Add(-int64(len(b)))
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
	subs := make([]*runtime.Subscription, 0, len(c.subs))
	for _, sub := range c.subs {
		subs = append(subs, sub)
	}
	c.subs = map[string]*runtime.Subscription{}
	terms := make([]*terminal.Subscription, 0, len(c.terms))
	for _, sub := range c.terms {
		terms = append(terms, sub)
	}
	c.terms = map[string]*terminal.Subscription{}
	c.bridge.mu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
	for _, sub := range terms {
		sub.Close()
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
	if err != nil || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	return c.enqueue(b)
}

// sendRaw 发送已序列化的帧，用于补发。
func (c *virtualConn) sendRaw(b []byte) bool {
	if len(b) == 0 || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	return c.enqueue(b)
}

func (c *virtualConn) enqueue(b []byte) bool {
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

func (c *virtualConn) reply(m protocol.Message) { c.send(m) }

// connContext 实现 connSink。
func (c *virtualConn) connContext() context.Context { return c.ctx }

// trackTerminal 实现 connSink。
func (c *virtualConn) trackTerminal(id string, sub *terminal.Subscription) {
	c.terms[id] = sub
}

// dropTerminal 实现 connSink。
func (c *virtualConn) dropTerminal(id string) {
	if sub, ok := c.terms[id]; ok {
		sub.Close()
		delete(c.terms, id)
	}
}
