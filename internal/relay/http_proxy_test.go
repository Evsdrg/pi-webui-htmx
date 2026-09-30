package relay

import (
	"context"
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

	tunnel := dialTunnelOrFail(t, srv, "dev-1", deviceToken)
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

// `/d/{id}` 必须规范化到 `/d/{id}/`：设备侧的相对 URL 只有在这个
// 文档地址下才会落回设备前缀之内。
func Test设备根规范化重定向(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, deviceToken := pairDevice(t, srv, users, "dev-1")
	tunnel := dialTunnelOrFail(t, srv, "dev-1", deviceToken)
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
	tunnel := dialTunnelOrFail(t, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()
	big := strings.NewReader(strings.Repeat("x", (4<<20)+16))
	if got := do(userToken, "/d/dev-1/ui/models/save", big); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大请求体应 413，实际 %d", got)
	}
}
