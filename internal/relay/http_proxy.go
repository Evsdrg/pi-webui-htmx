package relay

import (
	"io"
	"net/http"
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
)

// HTTPEnvelope 是隧道里的一个 HTTP 请求或响应。
// relay 只读 ID 做配对，其余字段对它是字节。
//
// 导出是因为隧道两端（relay 与桥）都要构造它；
// 桥只应通过 MarshalHTTPFrame 发送，避免自己拼 JSON 键名。
type HTTPEnvelope struct {
	ID      string      `json:"id"`
	Method  string      `json:"method,omitempty"`
	Path    string      `json:"path,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
	Status  int         `json:"status,omitempty"`
	// Error 只在设备侧拒绝或出错时出现，供 relay 回一个可读状态。
	Error string `json:"error,omitempty"`
}

// pendingHTTP 是一次等待响应的转发。
type pendingHTTP struct {
	ch chan HTTPEnvelope
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
		writeRelayError(w, http.StatusRequestEntityTooLarge,
			protocol.E("limit_exceeded", "云端转发暂不支持超过 4 MiB 的请求体"))
		return
	}
	envelope := HTTPEnvelope{
		ID:      newHTTPID(),
		Method:  r.Method,
		Path:    path,
		Headers: collectHeaders(r),
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

	waiter := s.registerHTTPWaiter(envelope.ID)
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

func (s *Server) registerHTTPWaiter(id string) *pendingHTTP {
	waiter := &pendingHTTP{ch: make(chan HTTPEnvelope, 1)}
	s.mu.Lock()
	s.httpWaitersInit()
	s.httpWaiters[id] = waiter
	s.mu.Unlock()
	return waiter
}

func (s *Server) dropHTTPWaiter(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.httpWaitersInit()
	delete(s.httpWaiters, id)
}

// deliverHTTP 把设备回的响应交给等待中的转发；没有等待者就丢弃。
func (s *Server) deliverHTTP(envelope HTTPEnvelope) bool {
	s.mu.Lock()
	s.httpWaitersInit()
	waiter := s.httpWaiters[envelope.ID]
	s.mu.Unlock()
	if waiter == nil {
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
func collectHeaders(r *http.Request) [][2]string {
	out := make([][2]string, 0, len(r.Header))
	for name, values := range r.Header {
		switch strings.ToLower(name) {
		case "host", "content-length", "connection", "upgrade", "keep-alive",
			"proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding",
			// Origin/Host 的校验必须在设备侧完成，不把浏览器的来源伪装成 relay 的。
			"origin", "referer", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto":
			continue
		}
		for _, value := range values {
			out = append(out, [2]string{name, value})
		}
	}
	return out
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
