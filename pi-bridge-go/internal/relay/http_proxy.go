package relay

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/protocol"
)

// 设备前缀的 HTTP 转发（B54）。
//
// 形态：浏览器访问 `{前缀}/d/{deviceId}/{路径}`，relay 把这次请求
// 封装成一条 HTTP 帧经隧道发给设备，设备在自己的 HTTP handler 上跑一遍，
// 再把响应回传。relay 依然**不解析业务协议**：它只看设备前缀与路由字段，
// HTTP 载荷对它是字节。
const (
	// devicePrefix 是设备前缀。前端在这个前缀下加载外壳、片段与资源。
	devicePrefix = "/d/"

	// maxHTTPBody 是转发请求体的上限。
	//
	// 它是**有意的**保守值：在没有分片之前，一条超大的请求体会把
	// 隧道单帧顶到上限之上（附件就是这个量级）。超限时明确回 413，
	// 而不是让隧道断连。分片与云端大文件上传是后续工作。
	maxHTTPBody = 4 << 20

	// httpProxyTimeout 覆盖「请求发出 → 响应回来」的全过程。
	httpProxyTimeout = 60 * time.Second

	// maxPendingHTTPWaiters 是在途转发的上限。每条在途转发占一个 goroutine
	// 与一个表项，最长 httpProxyTimeout（60s）；不设上限时，任何已认证用户
	// 都能开无限并发把它撑爆。超限明确回 503，而不是无界增长。
	maxPendingHTTPWaiters = 256
)

// HTTPEnvelope 是隧道里的一个 HTTP 请求或响应。
// relay 只读 ID 做配对，其余字段对它是字节。
//
// 导出是因为隧道两端（relay 与桥）都要构造它；
// 桥只应通过 MarshalHTTPFrame 发送，避免自己拼 JSON 键名。
type HTTPEnvelope struct {
	ID string `json:"id"`
	// Mount 是外部挂载前缀（例如 `/d/dev-1`），设备渲染外壳时用它生成
	// 文档基地址。它只经隧道传递，不放进 HTTP 头——头可以被伪造，
	// 而基地址一旦指向外部域就等于让外壳加载攻击者的脚本。
	Mount   string      `json:"mount,omitempty"`
	Method  string      `json:"method,omitempty"`
	Path    string      `json:"path,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
	Status  int         `json:"status,omitempty"`
	// Error 只在设备侧拒绝或出错时出现，供 relay 回一个可读状态。
	Error string `json:"error,omitempty"`
}

// pendingHTTP 是一次等待响应的转发。
// deviceID 记录发起这次转发的设备：响应帧只能由同一台设备的隧道交回。
// 否则 ID（http-N，全局自增、可枚举）可被任何持有合法设备令牌的其它设备
// 用同键伪造一条响应，注入到别的浏览器（跨用户、跨设备）。
type pendingHTTP struct {
	deviceID string
	ch       chan HTTPEnvelope
}

func (s *Server) httpWaitersInit() {
	if s.httpWaiters == nil {
		s.httpWaiters = map[string]*pendingHTTP{}
	}
}

// serveDeviceHTTP 处理 `{前缀}/d/{deviceId}/…` 的请求。
func (s *Server) serveDeviceHTTP(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.userAuth(r)
	if !ok {
		writeRelayError(w, http.StatusUnauthorized, protocol.E("unauthorized", "请先登录"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, devicePrefix)
	deviceID, path, found := strings.Cut(rest, "/")
	if deviceID == "" {
		writeRelayError(w, http.StatusBadRequest, protocol.E("invalid_params", "设备地址格式为 /d/{deviceId}/{路径}"))
		return
	}
	if !found {
		// `/d/{id}` 规范化为 `/d/{id}/`：设备侧的相对 URL（模板与前端脚本
		// 里的 assets/ui/… 都是相对当前文档解析的）只有在文档以斜杠结尾
		// 时才指向设备前缀之内。
		target := devicePrefix + deviceID + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return
	}
	// 空路径就是设备根：`/d/{id}/` 取的是它的外壳。
	// 在这里补斜杠，让设备侧收到的永远是绝对路径。
	if path == "" {
		path = "/"
	} else {
		path = "/" + path
	}
	// 设备前缀下的 WebSocket：浏览器连的是 `{前缀}/api/v1/ws`。
	// 这里把它接到与 `/client` 完全相同的连接管理上——前端因此不必知道
	// 自己在本地还是云端形态（B54）。clientId 由 relay 生成：
	// 它只是 relay 与桥之间的路由标识，浏览器不需要看到。
	if isWebSocketUpgrade(r) && strings.HasSuffix(r.URL.Path, "/api/v1/ws") {
		s.serveDeviceWS(w, r, deviceID)
		return
	}
	// 归属校验与 WS 入口一致：只有设备所属用户可以经隧道访问它。
	deviceOwner, err := s.registry.Owner(deviceID)
	if err != nil || deviceOwner != owner {
		writeRelayError(w, http.StatusForbidden, protocol.E("forbidden", "无权访问该设备"))
		return
	}
	s.mu.Lock()
	tunnel := s.tunnels[deviceID]
	s.mu.Unlock()
	if tunnel == nil {
		writeRelayError(w, http.StatusBadGateway, protocol.E("device_offline", "设备当前不在线"))
		return
	}

	body, err := readLimited(r.Body, maxHTTPBody)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == "limit_exceeded" {
			writeRelayError(w, http.StatusRequestEntityTooLarge, pe)
			return
		}
		// 读请求体失败多半是客户端超时或中断，不是体积超限——
		// 一律回 413 会把慢上传误报成「超过 4 MiB」。
		writeRelayError(w, http.StatusBadRequest, protocol.E("invalid_request", "读取请求体失败"))
		return
	}
	envelope := HTTPEnvelope{
		ID:      newHTTPID(),
		Mount:   devicePrefix + deviceID,
		Method:  r.Method,
		Path:    path,
		Headers: s.collectHeaders(r),
		Body:    body,
	}
	if raw := r.URL.RawQuery; raw != "" {
		envelope.Path += "?" + raw
	}
	frame, err := marshalRouteFrame(routeFrame{HTTP: &envelope})
	if err != nil {
		writeRelayError(w, http.StatusInternalServerError, protocol.E("pi_error", "无法编码转发请求"))
		return
	}

	waiter := s.registerHTTPWaiter(envelope.ID, deviceID)
	if waiter == nil {
		// 在途转发已达上限：每位已认证用户都能开无限并发请求，每条占用一个
		// goroutine 与表项最多 60 秒，必须有上限并明确拒绝（WS 侧有连接上限）。
		writeRelayError(w, http.StatusServiceUnavailable, protocol.E("busy", "云端转发并发已满，请稍后重试"))
		return
	}
	defer s.dropHTTPWaiter(envelope.ID)
	tunnel.out.send(frame, tunnel.cancel)

	select {
	case reply := <-waiter.ch:
		if reply.Error != "" {
			writeRelayError(w, http.StatusBadGateway, protocol.E("device_error", reply.Error))
			return
		}
		writeHTTPReply(w, reply)
	case <-time.After(httpProxyTimeout):
		writeRelayError(w, http.StatusGatewayTimeout, protocol.E("timeout", "设备未在期限内应答"))
	case <-r.Context().Done():
		// 浏览器断开：不用回包，隧道侧会在自己的超时里收尾。
	}
}

// registerHTTPWaiter 登记一个等待响应的转发；在途数量达上限时返回 nil。
func (s *Server) registerHTTPWaiter(id, deviceID string) *pendingHTTP {
	waiter := &pendingHTTP{deviceID: deviceID, ch: make(chan HTTPEnvelope, 1)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpWaitersInit()
	if len(s.httpWaiters) >= maxPendingHTTPWaiters {
		return nil
	}
	s.httpWaiters[id] = waiter
	return waiter
}

func (s *Server) dropHTTPWaiter(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpWaitersInit()
	delete(s.httpWaiters, id)
}

// deliverHTTP 把设备回的响应交给等待中的转发。
// deviceID 是交出这条响应的设备；只有与该转发发起设备一致时才投递——
// 否则任何其它设备都能用可枚举的 ID 冒充目标设备回包。不一致或没有
// 等待者时丢弃。
func (s *Server) deliverHTTP(deviceID string, envelope HTTPEnvelope) bool {
	s.mu.Lock()
	s.httpWaitersInit()
	waiter := s.httpWaiters[envelope.ID]
	s.mu.Unlock()
	if waiter == nil || waiter.deviceID != deviceID {
		return false
	}
	select {
	case waiter.ch <- envelope:
	default:
	}
	return true
}

// readLimited 读取至多 limit 字节；超出即报错（调用方回 413）。
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, protocol.E("limit_exceeded", "请求体超过转发上限")
	}
	return buf, nil
}

// collectHeaders 收集转发需要的头。逐跳头与 Host 不转发：
// 它们是连接层面的，交给设备侧自己决定。
//
// 另外剥掉 **relay 自己的账号凭据**：relay 会话 Cookie 与用户令牌只用于
// relay 的登录，桥不认识也用不到它们。不剥离就会把它们随请求转发到设备，
// 让设备主机拿到一个可 list/claim/revoke 该用户全部设备的账号级凭据——
// 这与「桥只持有模型密钥、不持有 relay 账号凭据」的边界相悖。
// 桥自己的 Cookie（pi_bridge_session）与桥令牌不是 relay 凭据，照常转发。
func (s *Server) collectHeaders(r *http.Request) [][2]string {
	out := make([][2]string, 0, len(r.Header))
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "host", "content-length", "connection", "upgrade", "keep-alive",
			"proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding",
			// Origin/Host 的校验必须在设备侧完成，不把浏览器的来源伪装成 relay 的。
			"origin", "referer", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
			continue
		case "authorization":
			// 只剥 relay 用户令牌；桥令牌（relay 校验不过）原样转发。
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token != "" {
				if _, ok := s.users.Check(token); ok {
					continue
				}
			}
		case "cookie":
			if stripped := stripRelayCookie(values); stripped != "" {
				out = append(out, [2]string{name, stripped})
			}
			continue
		}
		for _, value := range values {
			out = append(out, [2]string{name, value})
		}
	}
	return out
}

// stripRelayCookie 去掉 Cookie 头里的 relay 会话 Cookie，保留其余。
func stripRelayCookie(values []string) string {
	var kept []string
	for _, raw := range values {
		for _, part := range strings.Split(raw, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if strings.EqualFold(strings.TrimSpace(name), userCookieName) {
				continue
			}
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

func writeHTTPReply(w http.ResponseWriter, reply HTTPEnvelope) {
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	for _, kv := range reply.Headers {
		// Set-Cookie 会有多条，托管方已按 [2]string 展开，逐条 Add。
		w.Header().Add(kv[0], kv[1])
	}
	w.WriteHeader(status)
	if len(reply.Body) > 0 {
		_, _ = w.Write(reply.Body)
	}
}

var httpIDSeq struct {
	sync.Mutex
	n uint64
}

func newHTTPID() string {
	httpIDSeq.Lock()
	defer httpIDSeq.Unlock()
	httpIDSeq.n++
	return "http-" + strconv.FormatUint(httpIDSeq.n, 10)
}

// MarshalHTTPFrame 把一条 HTTP 帧编码成隧道帧（桥用它回响应）。
func MarshalHTTPFrame(envelope HTTPEnvelope) ([]byte, error) {
	env := envelope
	return marshalRouteFrame(routeFrame{HTTP: &env})
}

// isWebSocketUpgrade 判断这是不是一次 WebSocket 升级请求。
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// serveDeviceWS 把设备前缀下的 WS 升级接到既有的浏览器连接管理上。
func (s *Server) serveDeviceWS(w http.ResponseWriter, r *http.Request, deviceID string) {
	clientID := r.URL.Query().Get("clientId")
	if clientID == "" {
		clientID = "d-" + newHTTPID()
	}
	query := url.Values{}
	query.Set("deviceId", deviceID)
	query.Set("clientId", clientID)
	forwarded := r.Clone(r.Context())
	forwarded.URL.Path = "/client"
	forwarded.URL.RawQuery = query.Encode()
	// 复用 /client 的认证、归属、连接上限与读写循环：两处各写一遍
	// 必然会漂移（B23 的连接上限就是在这种复制里漏掉过一次）。
	s.handleClient(w, forwarded)
}
