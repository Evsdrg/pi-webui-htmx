package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pairDevice 走一遍配对流程，返回用户令牌与设备令牌。
func pairDevice(t *testing.T, srv *httptest.Server, users *Users, deviceID string) (userToken, deviceToken string) {
	t.Helper()
	userToken, err := users.AddUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := users.AddDeviceSecret(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	_, out := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"`+deviceID+`","secret":"`+secret+`"}`, "")
	code, _ := out["pairingCode"].(string)
	if code == "" {
		t.Fatalf("未拿到配对码: %v", out)
	}
	_, out2 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, userToken)
	deviceToken, _ = out2["deviceToken"].(string)
	if deviceToken == "" {
		t.Fatalf("未拿到设备令牌: %v", out2)
	}
	return userToken, deviceToken
}

// B54：设备前缀的 HTTP 转发——浏览器请求经隧道到设备，响应按 ID 回配。
func Test设备前缀HTTP转发(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")

	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()

	// 设备侧（测试里手工扮演）：读一条 HTTP 帧，回一个响应。
	seen := make(chan HTTPEnvelope, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, frame, err := tunnel.Read(ctx)
		if err != nil {
			return
		}
		rf, ok := Unwrap(frame)
		if !ok || rf.HTTP == nil {
			return
		}
		seen <- *rf.HTTP
		raw, err := MarshalHTTPFrame(HTTPEnvelope{
			ID: rf.HTTP.ID, Status: http.StatusOK,
			Headers: [][2]string{{"Content-Type", "text/plain; charset=utf-8"}, {"Set-Cookie", "pi_session=abc; Path=/"}},
			Body:    []byte("来自桥的响应"),
		})
		if err == nil {
			_ = tunnel.Write(ctx, websocket.MessageText, raw)
		}
	}()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/d/dev-1/ui/sessions?limit=5", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Cookie", "pi_session=from-browser")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "来自桥的响应" {
		t.Fatalf("转发响应异常: %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Set-Cookie"); !strings.Contains(got, "pi_session=abc") {
		t.Fatalf("Set-Cookie 未透传: %q", got)
	}

	got := <-seen
	if got.Method != http.MethodGet || got.Path != "/ui/sessions?limit=5" {
		t.Fatalf("设备收到的请求不对: %+v", got)
	}
	// 浏览器的 Cookie 必须原样带给设备（桥用它做会话鉴权）。
	var sawCookie bool
	for _, kv := range got.Headers {
		if strings.EqualFold(kv[0], "Cookie") && strings.Contains(kv[1], "from-browser") {
			sawCookie = true
		}
		// Host/Origin 属于连接与来源语义，不转发。
		if strings.EqualFold(kv[0], "Host") || strings.EqualFold(kv[0], "Origin") {
			t.Fatalf("不该转发 %s: %+v", kv[0], got.Headers)
		}
	}
	if !sawCookie {
		t.Fatalf("浏览器 Cookie 未转发: %+v", got.Headers)
	}
}

// 另一台设备的隧道不能替目标设备回包：HTTP 响应的等待者必须绑定发起设备。
// 反例：ID（http-N）全局可枚举，任何持有合法设备令牌的其它设备用同键
// 写一条伪响应，就能把任意状态码/头/正文注入到别人的浏览器。
func Test中继拒绝跨设备伪造的HTTP响应(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userTokenA, deviceTokenA := pairDevice(t, srv, users, "dev-a")
	_, deviceTokenB := pairDevice(t, srv, users, "dev-b")

	tunnelA := dialTunnelOrFail(t, s, srv, "dev-a", deviceTokenA)
	defer tunnelA.CloseNow()
	tunnelB := dialTunnelOrFail(t, s, srv, "dev-b", deviceTokenB)
	defer tunnelB.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 浏览器请求设备 A；设备 A 先扣住不回，等设备 B 来抢答。
	got := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/d/dev-a/", nil)
		req.Header.Set("Authorization", "Bearer "+userTokenA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- "ERR:" + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()

	// 设备 A 读出待处理的转发 ID。
	_, frame, err := tunnelA.Read(ctx)
	if err != nil {
		t.Fatalf("设备 A 未收到帧: %v", err)
	}
	rf, ok := Unwrap(frame)
	if !ok || rf.HTTP == nil {
		t.Fatalf("不是 HTTP 帧: %s", frame)
	}
	injected := "被设备 B 伪造的响应"
	raw, err := MarshalHTTPFrame(HTTPEnvelope{ID: rf.HTTP.ID, Status: http.StatusOK, Body: []byte(injected)})
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnelB.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("设备 B 写入失败: %v", err)
	}

	// 设备 B 的伪响应不该被投递：浏览器要么拿不到注入内容，要么等价地
	// 拿到真实的转发超时/设备 A 的真实回复。这里断言它绝不等于注入内容。
	select {
	case body := <-got:
		if body == injected {
			t.Fatalf("跨设备响应注入成功：设备 B 冒充设备 A 回包")
		}
	case <-time.After(2 * time.Second):
	}

	// 设备 A 现在给真实响应，浏览器必须收到它，证明同设备回包仍正常。
	real, err := MarshalHTTPFrame(HTTPEnvelope{ID: rf.HTTP.ID, Status: http.StatusOK, Body: []byte("设备 A 的真实响应")})
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnelA.Write(ctx, websocket.MessageText, real); err != nil {
		t.Fatalf("设备 A 写入失败: %v", err)
	}
	select {
	case body := <-got:
		if body != "设备 A 的真实响应" {
			t.Fatalf("设备 A 的真实响应未生效: %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("设备 A 的真实响应未到达浏览器")
	}
}

// relay 自己的账号凭据（会话 Cookie 与用户令牌）不得随请求转发到设备：
// 设备主机只应持有模型密钥，不该额外持有可 list/claim/revoke 该用户全部
// 设备的 relay 账号级凭据。桥自己的 Cookie 与令牌不是 relay 凭据，照常转发。
func Test中继不转发自身凭据(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")
	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()

	seen := make(chan HTTPEnvelope, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, frame, err := tunnel.Read(ctx)
		if err != nil {
			return
		}
		rf, ok := Unwrap(frame)
		if !ok || rf.HTTP == nil {
			return
		}
		seen <- *rf.HTTP
		raw, _ := MarshalHTTPFrame(HTTPEnvelope{ID: rf.HTTP.ID, Status: http.StatusOK, Body: []byte("ok")})
		_ = tunnel.Write(ctx, websocket.MessageText, raw)
	}()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/d/dev-1/ui/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	// 同时带上 relay 会话 Cookie 与桥自己的 Cookie。
	req.Header.Set("Cookie", "pi_relay_session=should-not-forward; pi_bridge_session=keep-me")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	got := <-seen
	var authorization, cookie string
	for _, kv := range got.Headers {
		switch strings.ToLower(kv[0]) {
		case "authorization":
			authorization = kv[1]
		case "cookie":
			cookie += kv[1]
		}
	}
	if strings.Contains(authorization, userToken) || authorization != "" {
		t.Fatalf("relay 用户令牌被转发到设备: %q", authorization)
	}
	if strings.Contains(cookie, "pi_relay_session") {
		t.Fatalf("relay 会话 Cookie 被转发到设备: %q", cookie)
	}
	if !strings.Contains(cookie, "pi_bridge_session=keep-me") {
		t.Fatalf("桥自己的 Cookie 应照常转发: %q", cookie)
	}
}

// /api/relay/devices 只回设备元数据，不回令牌哈希（内部校验值，浏览器用不到）。
func Test设备列表不暴露令牌哈希(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, _ := pairDevice(t, srv, users, "dev-1")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/relay/devices", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "tokenHash") {
		t.Fatalf("设备列表不应包含令牌哈希: %s", body)
	}
}

// 在途转发达上限时 registerHTTPWaiter 返回 nil（调用方回 503），而不是无界增长。
func TestHTTP等待者达上限被拒绝(t *testing.T) {
	s, _, _ := newRelayServer(t)
	s.mu.Lock()
	s.httpWaitersInit()
	for i := 0; i < maxPendingHTTPWaiters; i++ {
		s.httpWaiters[fmt.Sprintf("w-%d", i)] = &pendingHTTP{ch: make(chan HTTPEnvelope, 1)}
	}
	s.mu.Unlock()
	if w := s.registerHTTPWaiter("overflow", "dev-1"); w != nil {
		t.Fatal("等待者达上限时应返回 nil")
	}
}

// `/d/{id}` 必须规范化到 `/d/{id}/`：设备侧的相对 URL 只有在这个
// 文档地址下才会落回设备前缀之内。
func Test设备根规范化重定向(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")
	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/d/dev-1", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("应 308 重定向，实际 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/d/dev-1/" {
		t.Fatalf("重定向目标不对: %q", loc)
	}
}

// 设备前缀入口的拒绝面：未登录、非归属用户、设备离线、超限的请求体。// 设备前缀入口的拒绝面：未登录、非归属用户、设备离线、超限的请求体。
func Test设备前缀HTTP转发的拒绝面(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")
	otherToken, err := users.AddUser("bob")
	if err != nil {
		t.Fatal(err)
	}
	_ = deviceToken

	do := func(token, path string, body io.Reader) int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if got := do("", "/d/dev-1/ui/sessions", nil); got != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", got)
	}
	if got := do(otherToken, "/d/dev-1/ui/sessions", nil); got != http.StatusForbidden {
		t.Fatalf("非归属用户应 403，实际 %d", got)
	}
	// 设备不在线（没有隧道连接）。
	if got := do(userToken, "/d/dev-1/ui/sessions", nil); got != http.StatusBadGateway {
		t.Fatalf("设备离线应 502，实际 %d", got)
	}
	// 缺少设备标识的地址是非法请求；
	// `/d/{id}`（有设备没路径）则是 308 规范化，由专门用例覆盖。
	if got := do(userToken, "/d/", nil); got != http.StatusBadRequest {
		t.Fatalf("缺少设备标识应 400，实际 %d", got)
	}

	// 设备在线时：超过转发上限的请求体明确回 413，而不是把隧道顶断。
	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()
	big := strings.NewReader(strings.Repeat("x", (4<<20)+16))
	if got := do(userToken, "/d/dev-1/ui/models/save", big); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应 413，实际 %d", got)
	}
}

// B54：设备前缀下的 WS 升级接到与 /client 相同的连接管理，
// 前端因此不必区分本地与云端形态。
func Test设备前缀下的WS连接(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")
	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()

	cookies := cookieJar(t, srv, userToken)
	if len(cookies) == 0 {
		t.Fatal("没有拿到 relay 会话 Cookie")
	}
	header := http.Header{}
	header.Set("Cookie", cookies[0].Name+"="+cookies[0].Value)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/d/dev-1/api/v1/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("设备前缀下的 WS 应能升级: %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"kind":"command","method":"worker.list"}`)); err != nil {
		t.Fatal(err)
	}
	_, got, err := tunnel.Read(ctx)
	if err != nil {
		t.Fatalf("隧道未收到浏览器帧: %v", err)
	}
	rf, ok := Unwrap(got)
	if !ok || rf.From == "" {
		t.Fatalf("隧道应收到带来源标识的路由帧: %s", got)
	}
	if string(rf.Data) != `{"kind":"command","method":"worker.list"}` {
		t.Fatalf("业务帧被改写: %s", rf.Data)
	}
}
