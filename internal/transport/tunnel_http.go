package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"pi-bridge-go/internal/relay"
	"pi-bridge-go/internal/workspace"
)

// 经隧道的 HTTP 转发（B54）：云端 relay 把浏览器的请求封装成 HTTP 帧，
// 桥在**自己的 HTTP handler** 上跑一遍，再把响应回传。
//
// 为什么不另开一条内部转发路径：外壳、片段、资源与 API 的状态码约定、
// 鉴权与限额都已经在 `Server.ServeHTTP` 里实现过一次；复制一份必然漂移。
//
// 信任模型与 WS 路径一致：relay 已做用户认证与设备归属校验，隧道里的
// 请求按「来自已认证浏览器」对待。因此这里显式给请求设上桥自己的 Host，
// 并**不带** Origin——桥的 Host/Origin 严格校验原样保留，不因为云端而放宽。
const (
	// maxTunnelHTTPResponse 是单次转发响应体的**下限**预算。
	// 超限时明确报错，而不是让隧道单帧顶到上限。
	maxTunnelHTTPResponse = 8 << 20

	// responseHeadroom 是响应预算相对内部内容上限留的余量：
	// 外壳、片段这类渲染结果比原始文件大（HTML 转义、模板包装）。
	responseHeadroom = 1 << 20

	tunnelHTTPTimeout = 60 * time.Second
)

// tunnelResponseBudget 按桥**实际配置**的内容上限算响应预算。
//
// 写死一个常量会在调大工作区读取上限之后出错：图片预览是
// `/ui/file-image` 直接回文件字节，它在云端要经 HTTP 转发，
// 转发预算比它小就会 502——而且只在云端形态出现，本地直连一切正常。
// 因此这里取「下限」与「内部最大内容 + 余量」的较大值。
func tunnelResponseBudget(files *workspace.Files) int64 {
	budget := int64(maxTunnelHTTPResponse)
	if files == nil {
		return budget
	}
	if want := files.MaxReadBytes() + responseHeadroom; want > budget {
		budget = want
	}
	return budget
}

// handleTunnelHTTP 处理一条来自 relay 的 HTTP 帧，返回是否已消费。
func (t *TunnelBridge) handleTunnelHTTP(ctx context.Context, envelope relay.HTTPEnvelope) bool {
	if t.sender == nil {
		return false
	}
	go func() {
		reply := t.roundTripHTTP(ctx, envelope)
		raw, err := relay.MarshalHTTPFrame(reply)
		if err != nil {
			return
		}
		if err := t.sender(raw); err != nil {
			// 隧道已断：没有接收方，丢弃即可。
			return
		}
	}()
	return true
}

// roundTripHTTP 在桥自己的 HTTP handler 上执行一次请求。
func (t *TunnelBridge) roundTripHTTP(ctx context.Context, envelope relay.HTTPEnvelope) relay.HTTPEnvelope {
	reply := relay.HTTPEnvelope{ID: envelope.ID}
	timeoutCtx, cancel := context.WithTimeout(ctx, tunnelHTTPTimeout)
	defer cancel()

	method := envelope.Method
	if method == "" {
		method = http.MethodGet
	}
	path := envelope.Path
	if path == "" || !strings.HasPrefix(path, "/") {
		reply.Error = "云端转发的路径不合法"
		return reply
	}
	reqCtx := context.WithValue(timeoutCtx, shellMountKey{}, envelope.Mount)
	req, err := http.NewRequestWithContext(reqCtx, method, "http://"+t.server.host+path, bytes.NewReader(envelope.Body))
	if err != nil {
		reply.Error = "无法构造请求"
		return reply
	}
	// Host 必须是桥自己的：relay 转发时已经剥掉浏览器侧的 Host/Origin，
	// 这里补上桥期望的值，Host 校验因此仍然生效。
	req.Host = t.server.host
	for _, kv := range envelope.Headers {
		if len(kv) != 2 {
			continue
		}
		req.Header.Add(kv[0], kv[1])
	}
	recorder := &frameRecorder{header: http.Header{}}
	t.server.ServeHTTP(recorder, req)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	body := recorder.body.Bytes()
	limit := t.maxHTTPResponse
	if limit <= 0 {
		limit = maxTunnelHTTPResponse
	}
	if int64(len(body)) > limit {
		reply.Status = http.StatusBadGateway
		reply.Headers = [][2]string{{"Content-Type", "text/plain; charset=utf-8"}}
		reply.Body = []byte("响应超过云端转发上限。这是当前云端形态的已知限制（分片尚未实现）。")
		return reply
	}
	reply.Status = status
	reply.Headers = responseHeaders(recorder.header)
	reply.Body = body
	return reply
}

// responseHeaders 挑出要回传的响应头：逐跳头与长度由 relay 侧自己决定。
func responseHeaders(header http.Header) [][2]string {
	out := make([][2]string, 0, len(header)+1)
	for name, values := range header {
		switch strings.ToLower(name) {
		case "content-length", "connection", "transfer-encoding", "keep-alive", "date":
			continue
		}
		for _, value := range values {
			out = append(out, [2]string{name, value})
		}
	}
	return out
}

// frameRecorder 收集一次 handler 执行的响应。
// 生产代码里不用 httptest：那是测试依赖，且它的语义（例如 Flush 行为）
// 与真实 ResponseWriter 并不完全一致。
type frameRecorder struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (f *frameRecorder) Header() http.Header { return f.header }

func (f *frameRecorder) WriteHeader(code int) {
	if f.wroteHeader {
		return
	}
	f.wroteHeader = true
	f.status = code
}

func (f *frameRecorder) Write(b []byte) (int, error) {
	if !f.wroteHeader {
		f.WriteHeader(http.StatusOK)
	}
	return f.body.Write(b)
}

var _ io.Writer = (*frameRecorder)(nil)

// shellMount 从隧道 HTTP 帧里取出外部挂载前缀。
//
// 它不走 HTTP 头：头可以由任意客户端伪造，而 `<base href>` 一旦被
// 引到外部域，外壳就会去加载攻击者的脚本。前缀只从隧道帧里读
// （relay 构造的帧），并且 DocBase 还会再校验一次形态。
func shellMount(r *http.Request) string {
	if r == nil {
		return ""
	}
	// 桥直连时不带前缀；经隧道时由 relay 在帧里给出，桥把它放进请求上下文。
	if v, ok := r.Context().Value(shellMountKey{}).(string); ok {
		return v
	}
	return ""
}

type shellMountKey struct{}
