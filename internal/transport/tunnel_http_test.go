package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"pi-bridge-go/internal/relay"
)

// B54：经隧道的 HTTP 帧在桥**自己的 handler** 上执行，
// 因此外壳、片段、资源与 API 的状态码与鉴权约定不需要第二份实现。
func Test隧道HTTP帧走桥自己的handler(t *testing.T) {
	s, _, _ := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)

	env := relay.HTTPEnvelope{ID: "h1", Method: http.MethodGet, Path: "/healthz"}
	raw, err := relay.MarshalHTTPFrame(env)
	if err != nil {
		t.Fatal(err)
	}
	if !bridge.HandleFrame(context.Background(), raw) {
		t.Fatal("HTTP 帧应被消费")
	}
	waitFrames(t, &mu, &sent, 1)
	reply := decodeHTTPReply(t, &mu, &sent, 0)
	if reply.Status != http.StatusOK {
		t.Fatalf("健康检查应 200，实际 %d（%s）", reply.Status, reply.Body)
	}
	if reply.ID != "h1" {
		t.Fatalf("响应必须带回请求 ID 供配对: %+v", reply)
	}
}

// 未认证的转发请求必须在桥侧同样被拒——信任 relay 不等于放弃桥的鉴权。
func Test隧道HTTP帧仍受桥鉴权约束(t *testing.T) {
	s, _, _ := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)

	// 不带凭据访问需要鉴权的 API。
	env := relay.HTTPEnvelope{ID: "h2", Method: http.MethodGet, Path: "/api/v1/sessions"}
	raw, _ := relay.MarshalHTTPFrame(env)
	bridge.HandleFrame(context.Background(), raw)
	waitFrames(t, &mu, &sent, 1)
	reply := decodeHTTPReply(t, &mu, &sent, 0)
	if reply.Status != http.StatusUnauthorized {
		t.Fatalf("无凭据应 401，实际 %d", reply.Status)
	}

	// 带上 relay 转发的会话 Cookie 之后应当放行（Cookie 路径）。
	cookieHeader := sessionCookieForTest(t, s)
	env2 := relay.HTTPEnvelope{ID: "h3", Method: http.MethodGet, Path: "/api/v1/sessions",
		Headers: [][2]string{{"Cookie", cookieHeader}}}
	raw2, _ := relay.MarshalHTTPFrame(env2)
	bridge.HandleFrame(context.Background(), raw2)
	waitFrames(t, &mu, &sent, 2)
	reply2 := decodeHTTPReply(t, &mu, &sent, 1)
	if reply2.Status != http.StatusOK {
		t.Fatalf("带 Cookie 应 200，实际 %d（%s）", reply2.Status, reply2.Body)
	}
}

// 响应超过转发上限时明确回错，而不是让隧道单帧顶到上限。
// 上限是字段：不同部署形态容忍度不同，测试里调小即可覆盖这条路径。
func Test隧道HTTP响应超限时明确报错(t *testing.T) {
	s, _, _ := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)
	bridge.maxHTTPResponse = 64

	env := relay.HTTPEnvelope{
		ID: "h4", Method: http.MethodGet, Path: "/api/v1/capabilities",
		Headers: [][2]string{{"Authorization", "Bearer " + testToken}},
	}
	raw, _ := relay.MarshalHTTPFrame(env)
	bridge.HandleFrame(context.Background(), raw)
	waitFrames(t, &mu, &sent, 1)
	reply := decodeHTTPReply(t, &mu, &sent, 0)
	if reply.Status != http.StatusBadGateway {
		t.Fatalf("超限响应应回 502，实际 %d（%s）", reply.Status, reply.Body)
	}
	if len(reply.Body) == 0 {
		t.Fatal("超限时应给出可读说明")
	}
}

// 隧道里的 HTTP 帧不能借头字段绕过桥的来源校验。
//
// 正常链路里 relay 会剥掉 Host/Origin；这里模拟绕过 relay 直接构造的帧：
//   - 伪 Host 无效：Go 的 Host 判定看 req.Host，而它由桥自己设定，
//     不是头里的 "Host"；
//   - 伪 Origin 会被桥**拒绝**——Origin 是普通头，桥的严格同源校验
//     照常生效，没有因为「信任 relay」而放宽。
func Test隧道HTTP帧无法伪装来源(t *testing.T) {
	s, _, _ := newTestServer(t)
	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)

	// 伪 Host：不影响桥的处理。
	hostOnly := relay.HTTPEnvelope{
		ID: "h5", Method: http.MethodGet, Path: "/api/v1/capabilities",
		Headers: [][2]string{{"Authorization", "Bearer " + testToken}, {"Host", "evil.example"}},
	}
	raw, _ := relay.MarshalHTTPFrame(hostOnly)
	bridge.HandleFrame(context.Background(), raw)
	waitFrames(t, &mu, &sent, 1)
	if reply := decodeHTTPReply(t, &mu, &sent, 0); reply.Status != http.StatusOK {
		t.Fatalf("Host 头不参与桥的判定，应正常处理，实际 %d（%s）", reply.Status, reply.Body)
	}

	// 伪 Origin：被桥拒绝。
	withOrigin := relay.HTTPEnvelope{
		ID: "h6", Method: http.MethodGet, Path: "/api/v1/capabilities",
		Headers: [][2]string{{"Authorization", "Bearer " + testToken}, {"Origin", "https://evil.example"}},
	}
	raw2, _ := relay.MarshalHTTPFrame(withOrigin)
	bridge.HandleFrame(context.Background(), raw2)
	waitFrames(t, &mu, &sent, 2)
	if reply := decodeHTTPReply(t, &mu, &sent, 1); reply.Status != http.StatusForbidden {
		t.Fatalf("跨源 Origin 应被桥拒绝，实际 %d", reply.Status)
	}
}

// decodeHTTPReply 取出第 idx 条回帧并解成 HTTP 帧。
func decodeHTTPReply(t *testing.T, mu *sync.Mutex, sent *[][]byte, idx int) relay.HTTPEnvelope {
	t.Helper()
	mu.Lock()
	frame := (*sent)[idx]
	mu.Unlock()
	rf, ok := relay.Unwrap(frame)
	if !ok || rf.HTTP == nil {
		t.Fatalf("回帧不是 HTTP 帧: %s", frame)
	}
	return *rf.HTTP
}

// sessionCookieForTest 用桥的令牌换一个会话 Cookie。
func sessionCookieForTest(t *testing.T, s *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth", nil)
	req.Host = s.host
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("换取 Cookie 失败: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("响应里没有会话 Cookie: %s", rec.Body.String())
	return ""
}
