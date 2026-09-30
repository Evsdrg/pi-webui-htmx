package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"pi-bridge-go/internal/protocol"
)

// requestScheme 推断请求的实际 scheme。
//
// 不能只看 r.TLS：文档要求 TLS 在反向代理终止，代理以 HTTP 回源时
// r.TLS 为 nil，浏览器发来的 https:// Origin 会被误判成跨源而拒绝，
// HTTPS 部署下 WS 根本连不上（B35）。因此信任 X-Forwarded-Proto，
// 但只接受明确的 https，其余值一律按 http 处理——不解析任意字符串，
// 避免客户端靠伪造头绕过 Origin 校验。
func requestScheme(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); fwd != "" {
		if strings.EqualFold(fwd, "https") {
			return "https"
		}
		return "http"
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// deviceTokenFromHeader 从 Authorization 头取设备令牌。
// 同时接受 "Bearer <token>" 与裸令牌两种形式。
func deviceTokenFromHeader(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	}
	// 不是 Bearer 形式时不猜：那可能是别的鉴权方案。
	if strings.Contains(value, " ") {
		return ""
	}
	return strings.TrimSpace(value)
}

// maxClientsPerOwner 限制单个用户同时持有的浏览器连接数。
// 多标签页是正常用法，但必须有个上限：公网 relay 上，
// 一个认证用户就能用大量 clientId 把 goroutine 与内存耗尽（B23）。
const maxClientsPerOwner = 16

// Server 是云端转发器的 HTTP/WS 入口。
// 只做鉴权、设备路由与字节搬运，不解析业务协议。
type Server struct {
	registry *Registry
	users    *Users
	host     string

	mu        sync.Mutex
	tunnels   map[string]*tunnelConn
	clients   map[string]*clientConn
	clientSeq uint64
	closed    bool
}

// tunnelConn 是一条来自本地桥的主动隧道。
type tunnelConn struct {
	deviceID string
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	out      chan []byte
	queued   atomic.Int64
}

// clientConn 是一条来自浏览器的连接。
type clientConn struct {
	deviceID string
	clientID string
	// owner 用于按用户统计连接数，防止单令牌耗尽 relay（B23）。
	owner  string
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	queued atomic.Int64
}

// routeFrame 是隧道与浏览器之间的最小路由封装。
// relay 只读 to/from 两个字段做转发，绝不解析 data 里的业务内容。
type routeFrame struct {
	To   string          `json:"to"`
	From string          `json:"from,omitempty"`
	Data json.RawMessage `json:"data"`
}

// RouteTo 把桥发来的原始帧包上目标浏览器标识（to），供隧道接入层调用。
func RouteTo(clientID string, payload []byte) []byte { return routeToBrowser(clientID, payload) }

// routeToBrowser 把桥发来的原始帧包上目标浏览器标识（to）。
// 桥必须用这个方向；relay 只认 to，缺失即丢弃，不做广播。
func routeToBrowser(clientID string, payload []byte) []byte {
	b, err := marshalRouteFrame(routeFrame{To: clientID, Data: json.RawMessage(payload)})
	if err != nil {
		return nil
	}
	return b
}

// routeFromBrowser 把浏览器发来的原始帧包上来源标识（from），
// 让桥知道该把回复发回哪个标签页。
func routeFromBrowser(clientID string, payload []byte) []byte {
	b, err := marshalRouteFrame(routeFrame{From: clientID, Data: json.RawMessage(payload)})
	if err != nil {
		return nil
	}
	return b
}

// marshalRouteFrame 编码路由封装。
//
// 不用 json.Marshal：它默认做 HTML 转义，把载荷里的 < > & 变成 \u003c 之类
// （最多 6 字节），于是「本身合法的帧」会在封装后顶出上限被丢掉。relay 只
// 转发、从不渲染，没有理由做 HTML 转义；关掉之后封装开销就是固定前缀，
// 与载荷内容无关。
func marshalRouteFrame(rf routeFrame) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rf); err != nil {
		return nil, err
	}
	// Encode 会补一个换行符；去掉它，让封装开销精确等于前缀加一。
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Unwrap 取出路由帧中的原始载荷与目标/来源。
func Unwrap(frame []byte) (routeFrame, bool) {
	if len(frame) == 0 || len(frame) > maxFrame {
		return routeFrame{}, false
	}
	var rf routeFrame
	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rf); err != nil || len(rf.Data) == 0 {
		return routeFrame{}, false
	}
	return rf, true
}

// Config 是转发器的入口约束。
type Config struct {
	// Host 是**精确的 Host 头值**（含端口，若对外带端口）。
	// 必填：没有它就无法把请求绑定到预期来源（B63）。
	Host string
}

// Validate 检查入口约束是否完整。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return protocol.E("invalid_params", "必须声明 --host：转发器只接受显式声明的来源")
	}
	if strings.ContainsAny(c.Host, " \t/") {
		return protocol.E("invalid_params", "--host 只能是主机名与可选端口，不含协议与路径")
	}
	return nil
}

// hostMatches 比较声明的主机与请求里的 Host。
//
// 语义：声明里**带端口**（含 `:`）就精确比对；不带端口则忽略请求里的端口。
// 放过端口是有意的——它不影响这套校验要挡的东西（DNS rebinding：
// 攻击者让浏览器访问解析到转发器 IP 的域名，Host 是那个域名而非本机名），
// 而端口是部署细节，写死会让同一实例换端口就整体失效。
// IPv6 字面量请写成带方括号与端口的形式（`[::1]:30143`）。
func hostMatches(declared, actual string) bool {
	if declared == "" {
		return false
	}
	if strings.Contains(declared, ":") {
		return declared == actual
	}
	host, _, err := net.SplitHostPort(actual)
	if err != nil {
		// 没有端口，直接比。
		return strings.EqualFold(declared, actual)
	}
	return strings.EqualFold(declared, host)
}

// NewServer 构造转发器。
//
// 与旧签名的差别是有意的：host 从「可选、为空就不校验」变成**必填**。
// 旧行为在 host 为空时把 Host 与 Origin 两道校验一起跳过，
// 于是任意来源都能访问公网 relay（DNS rebinding 与反代滥用的入口，B63）。
func NewServer(registry *Registry, users *Users, cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Server{
		registry: registry,
		users:    users,
		host:     cfg.Host,
		tunnels:  map[string]*tunnelConn{},
		clients:  map[string]*clientConn{},
	}, nil
}

// Close 关闭全部隧道与连接。
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	conns := make([]*tunnelConn, 0, len(s.tunnels))
	for _, t := range s.tunnels {
		conns = append(conns, t)
	}
	s.tunnels = map[string]*tunnelConn{}
	clients := make([]*clientConn, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.clients = map[string]*clientConn{}
	s.mu.Unlock()
	for _, t := range conns {
		t.cancel()
		t.ws.Close(websocket.StatusNormalClosure, "relay shutting down")
	}
	// 浏览器连接也必须关闭：只关 tunnels 会让已连接的页面一直挂到对端超时，
	// graceful shutdown 期间用户看不到任何关闭信号（B41）。
	for _, c := range clients {
		c.cancel()
		c.ws.Close(websocket.StatusNormalClosure, "relay shutting down")
	}
}

// dropDevice 中断某设备的全部隧道与浏览器连接。
func (s *Server) dropDevice(deviceID string) {
	s.mu.Lock()
	tunnel := s.tunnels[deviceID]
	if tunnel != nil {
		delete(s.tunnels, deviceID)
	}
	clients := make([]*clientConn, 0, 4)
	for k, c := range s.clients {
		if c.deviceID == deviceID {
			clients = append(clients, c)
			delete(s.clients, k)
		}
	}
	s.mu.Unlock()
	if tunnel != nil {
		tunnel.cancel()
		tunnel.ws.Close(websocket.StatusPolicyViolation, "device revoked")
	}
	for _, c := range clients {
		c.cancel()
		c.ws.Close(websocket.StatusPolicyViolation, "device revoked")
	}
	s.registry.SetOnline(deviceID, false)
}

// Stats 返回转发器状态。
func (s *Server) Stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"tunnels": len(s.tunnels),
		"clients": len(s.clients),
		"devices": s.registry.Stats(),
		"users":   s.users.Stats(),
	}
}

// userAuth 从请求中取出用户名：Bearer 用户令牌或会话 Cookie。
func (s *Server) userAuth(r *http.Request) (string, bool) {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if value != r.Header.Get("Authorization") {
		if owner, ok := s.users.Check(value); ok {
			return owner, true
		}
	}
	c, err := r.Cookie(userCookieName)
	if err != nil {
		return "", false
	}
	return s.users.CheckCookie(c.Value)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 构造期已保证 host 非空，所以这两道校验**不再有「跳过」分支**（B63）。
	if !hostMatches(s.host, r.Host) {
		writeRelayError(w, 403, protocol.E("host_denied", "Host 不在预期范围内"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Scheme, requestScheme(r)) || !hostMatches(s.host, parsed.Host) {
			writeRelayError(w, 403, protocol.E("origin_denied", "未启用跨源访问"))
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		writeRelayJSON(w, 200, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/relay/auth":
		s.handleAuth(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/relay/pair":
		s.handlePair(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/relay/claim":
		s.handleClaim(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/relay/devices":
		s.handleDevices(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/relay/revoke":
		s.handleRevoke(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/tunnel":
		s.handleTunnel(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/client":
		s.handleClient(w, r)
	default:
		writeRelayError(w, 404, protocol.E("not_found", "接口不存在"))
	}
}

func writeRelayJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRelayError(w http.ResponseWriter, status int, err error) {
	writeRelayJSON(w, status, protocol.Reply("", nil, err))
}

func relayErrorStatus(err error) int {
	status := 500
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "invalid_params":
			status = 400
		case "forbidden":
			status = 403
		case "not_found":
			status = 404
		case "conflict", "busy":
			status = 409
		case "limit_exceeded":
			status = 429
		case "unauthorized":
			status = 401
		}
	}
	return status
}

func decodeRelay(r *http.Request, dst any) error {
	if r.Body == nil {
		return protocol.E("invalid_params", "缺少请求体")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return protocol.E("invalid_params", "请求体不是合法的 JSON 对象")
	}
	var extra any
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return protocol.E("invalid_params", "一次只能发送一个 JSON 值")
	}
	return nil
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Token string `json:"token"`
	}
	if err := decodeRelay(r, &p); err != nil {
		writeRelayError(w, 400, err)
		return
	}
	owner, ok := s.users.Check(p.Token)
	if !ok {
		writeRelayError(w, 401, protocol.E("unauthorized", "用户令牌无效"))
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	exp := strconv.FormatInt(expires.Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name: userCookieName, Value: s.users.SignCookie(exp, owner), HttpOnly: true,
		Secure: requestScheme(r) == "https", SameSite: http.SameSiteStrictMode,
		Path: "/", Expires: expires, MaxAge: 8 * 60 * 60,
	})
	writeRelayJSON(w, 200, map[string]any{"ok": true, "owner": owner})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	// 配对登记由本地桥发起，用设备 ID + 预共享密钥鉴权。
	var p struct {
		DeviceID string `json:"deviceId"`
		Secret   string `json:"secret"`
		Name     string `json:"name"`
	}
	if err := decodeRelay(r, &p); err != nil {
		writeRelayError(w, 400, err)
		return
	}
	if p.DeviceID == "" || p.Secret == "" {
		writeRelayError(w, 400, protocol.E("invalid_params", "deviceId 与 secret 均不能为空"))
		return
	}
	if !s.users.CheckPairSecret(p.DeviceID, p.Secret) {
		writeRelayError(w, 401, protocol.E("unauthorized", "设备密钥无效"))
		return
	}
	code, err := s.registry.Register(p.DeviceID, p.Name)
	if err != nil {
		writeRelayError(w, relayErrorStatus(err), err)
		return
	}
	writeRelayJSON(w, 200, map[string]any{"deviceId": p.DeviceID, "pairingCode": code, "expiresInSeconds": int(s.registry.limits.ClaimTTL.Seconds())})
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.userAuth(r)
	if !ok {
		writeRelayError(w, 401, protocol.E("unauthorized", "需要用户身份"))
		return
	}
	var p struct {
		PairingCode string `json:"pairingCode"`
	}
	if err := decodeRelay(r, &p); err != nil {
		writeRelayError(w, 400, err)
		return
	}
	device, token, err := s.registry.Claim(owner, p.PairingCode)
	if err != nil {
		writeRelayError(w, relayErrorStatus(err), err)
		return
	}
	// 令牌只返回一次，relay 侧只保留哈希。
	if err := s.registry.SetDeviceToken(device.DeviceID, token); err != nil {
		writeRelayError(w, relayErrorStatus(err), err)
		return
	}
	writeRelayJSON(w, 200, map[string]any{
		"deviceId": device.DeviceID, "name": device.Name,
		"deviceToken": token, "owner": owner,
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.userAuth(r)
	if !ok {
		writeRelayError(w, 401, protocol.E("unauthorized", "需要用户身份"))
		return
	}
	writeRelayJSON(w, 200, map[string]any{"devices": s.registry.List(owner)})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.userAuth(r)
	if !ok {
		writeRelayError(w, 401, protocol.E("unauthorized", "需要用户身份"))
		return
	}
	var p struct {
		DeviceID string `json:"deviceId"`
	}
	if err := decodeRelay(r, &p); err != nil {
		writeRelayError(w, 400, err)
		return
	}
	if err := s.registry.Revoke(owner, p.DeviceID); err != nil {
		writeRelayError(w, relayErrorStatus(err), err)
		return
	}
	// 撤销必须立刻生效：中断该设备的活跃隧道与浏览器连接，
	// 不能只等它自己掉线。
	s.dropDevice(p.DeviceID)
	writeRelayJSON(w, 200, map[string]any{"revoked": true})
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("deviceId")
	// 设备令牌优先取 Authorization 头。查询串会被反向代理、
	// 浏览器历史与服务端访问日志原样记录，长期令牌不该出现在那里（B24）。
	token := deviceTokenFromHeader(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if deviceID == "" || token == "" {
		writeRelayError(w, 401, protocol.E("unauthorized", "缺少设备凭据"))
		return
	}
	if _, err := s.registry.AuthenticateDevice(deviceID, token); err != nil {
		writeRelayError(w, relayErrorStatus(err), err)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxFrame)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if old, ok := s.tunnels[deviceID]; ok {
		// 同设备重连：旧隧道立即让位，避免两处同时写入。
		old.cancel()
		delete(s.tunnels, deviceID)
		s.registry.SetOnline(deviceID, false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnelConn{
		deviceID: deviceID, ws: ws, ctx: ctx, cancel: cancel,
		out: make(chan []byte, 64),
	}
	s.tunnels[deviceID] = t
	s.mu.Unlock()
	s.registry.SetOnline(deviceID, true)
	defer func() {
		s.mu.Lock()
		if s.tunnels[deviceID] == t {
			delete(s.tunnels, deviceID)
		}
		s.mu.Unlock()
		s.registry.SetOnline(deviceID, false)
	}()
	go pumpWrites(ctx, ws, t.out, &t.queued)
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		// 桥侧帧带 to 字段指定目标浏览器；缺失或目标不在线时丢弃，不做广播。
		frame := make([]byte, len(b))
		copy(frame, b)
		rf, ok := Unwrap(frame)
		if !ok || rf.To == "" {
			continue
		}
		s.mu.Lock()
		target := s.findClientLocked(deviceID, rf.To)
		s.mu.Unlock()
		if target == nil {
			continue
		}
		enqueue(target.out, &target.queued, rf.Data, target.cancel)
	}
}

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.userAuth(r)
	if !ok {
		writeRelayError(w, 401, protocol.E("unauthorized", "需要用户身份"))
		return
	}
	deviceID := r.URL.Query().Get("deviceId")
	clientID := r.URL.Query().Get("clientId")
	if deviceID == "" || clientID == "" {
		writeRelayError(w, 400, protocol.E("invalid_params", "缺少 deviceId 或 clientId"))
		return
	}
	if len(clientID) > 64 {
		writeRelayError(w, 400, protocol.E("invalid_params", "clientId 过长"))
		return
	}
	deviceOwner, err := s.registry.Owner(deviceID)
	if err != nil || deviceOwner != owner {
		writeRelayError(w, 403, protocol.E("forbidden", "该设备不属于当前用户"))
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxFrame)
	// 关闭状态、设备在线、每 owner 连接数三项在同一段持锁区间内判定：
	// 拆成多段既要重复加锁，也会让判定之间出现窗口。
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.tunnels[deviceID] == nil {
		s.mu.Unlock()
		ws.Close(websocket.StatusTryAgainLater, "device offline")
		return
	}
	// 每 owner 连接上限：公网 relay 上，一个认证用户就能用大量
	// clientId 把 goroutine 与内存耗尽（B23）。
	perOwner := 0
	for _, existing := range s.clients {
		if existing.owner == owner {
			perOwner++
		}
	}
	if perOwner >= maxClientsPerOwner {
		s.mu.Unlock()
		ws.Close(websocket.StatusTryAgainLater, "too many connections")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &clientConn{
		deviceID: deviceID, clientID: clientID, owner: owner, ws: ws, ctx: ctx, cancel: cancel,
		out: make(chan []byte, 64),
	}
	if old := s.findClientLocked(deviceID, clientID); old != nil {
		// 同一 clientId 重连：旧连接立即让位，避免两处同时写入。
		old.cancel()
		for k, v := range s.clients {
			if v == old {
				delete(s.clients, k)
			}
		}
	}
	s.clientSeq++
	s.clients[deviceID+"#"+strconv.FormatUint(s.clientSeq, 10)] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		for k, v := range s.clients {
			if v == c {
				delete(s.clients, k)
			}
		}
		s.mu.Unlock()
	}()
	go pumpWrites(ctx, ws, c.out, &c.queued)
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		frame := make([]byte, len(b))
		copy(frame, b)
		// 本地桥不在线时丢弃，不让浏览器以为已送达。
		s.mu.Lock()
		t := s.tunnels[deviceID]
		s.mu.Unlock()
		if t == nil {
			continue
		}
		wrapped := routeFromBrowser(clientID, frame)
		if wrapped == nil {
			continue
		}
		enqueue(t.out, &t.queued, wrapped, t.cancel)
	}
}

// findClientLocked 按设备与客户端标识查找连接；调用方需持有锁。
func (s *Server) findClientLocked(deviceID, clientID string) *clientConn {
	for _, c := range s.clients {
		if c.deviceID == deviceID && c.clientID == clientID {
			return c
		}
	}
	return nil
}

// enqueue 把一帧放入有界队列；满了就关闭该连接，绝不在 relay 里无限堆积。
//
// 放行线取「队列预算」与本帧大小的较大值：桥允许的最大帧（图片附件量级）
// 不能被 4 MiB 的队列预算自己挤掉——否则最大的那批帧永远发不出去。队列
// 非空时仍按预算约束堆积，慢消费者不会因为这条放宽而无限占内存。
func enqueue(out chan []byte, queued *atomic.Int64, frame []byte, cancel context.CancelFunc) {
	size := int64(len(frame))
	limit := int64(maxQueueBytes)
	if size > limit {
		limit = size
	}
	if queued.Add(size) > limit {
		queued.Add(-size)
		cancel()
		return
	}
	select {
	case out <- frame:
	case <-time.After(enqueueTimeout):
		queued.Add(-int64(len(frame)))
		cancel()
	}
}

// pumpWrites 是单连接唯一写协程，保证 WS 写入串行化。
func pumpWrites(ctx context.Context, ws *websocket.Conn, out chan []byte, queued *atomic.Int64) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-out:
			queued.Add(-int64(len(b)))
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

const (
	// maxFrame 是 relay 允许的单帧上限。
	//
	// 它必须装得下桥允许浏览器发的最大帧（图片附件就是这个量级），再加上
	// 转发时外面那层路由封装——relay 是转发器，比桥更小气只会把合法帧变成
	// 「断隧道」（B53：这里曾写死 1 MiB）。
	maxFrame       = protocol.BrowserFrameLimit + protocol.RelayEnvelopeBytes
	maxQueueBytes  = 4 << 20
	enqueueTimeout = 2 * time.Second
	writeTimeout   = 10 * time.Second
	userCookieName = "pi_relay_session"
)
