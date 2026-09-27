package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialTunnelWithHeader 用指定 Authorization 头建隧道连接，失败返回 nil。
func dialTunnelWithHeader(t *testing.T, srv *httptest.Server, deviceID, auth string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel?deviceId=" + deviceID
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{auth}},
	})
	if err != nil {
		return nil
	}
	return conn
}

// Test隧道令牌可走Authorization头 覆盖 B24：
// 长期设备令牌放在查询串里会进入反向代理与访问日志。
// 现在优先取 Authorization 头；查询串仍接受以兼容旧桥。
func Test隧道令牌可走Authorization头(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	ut, _ := users.AddUser("alice")
	ds, _ := users.AddDeviceSecret("dev-1")
	_, out := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"`+ds+`"}`, "")
	code, _ := out["pairingCode"].(string)
	_, out2 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, ut)
	dt, _ := out2["deviceToken"].(string)
	if dt == "" {
		t.Fatal("没有拿到设备令牌")
	}
	// 头形式必须能建连。
	if c := dialTunnelWithHeader(t, srv, "dev-1", "Bearer "+dt); c != nil {
		c.CloseNow()
	} else {
		t.Fatal("Authorization 头形式的隧道连接失败")
	}
	// 查询串形式仍被接受（兼容已有部署）。
	if c := dialTunnelOrFail(t, srv, "dev-1", dt); c != nil {
		c.CloseNow()
	} else {
		t.Fatal("查询串形式的隧道连接失败")
	}
	// 错误的头必须被拒绝。
	if c := dialTunnelWithHeader(t, srv, "dev-1", "Bearer wrong"); c != nil {
		c.CloseNow()
		t.Fatal("错误的令牌被接受了")
	}
}

// Test反代HTTPS回源时Origin校验通过 覆盖 B35：
// TLS 在反向代理终止、以 HTTP 回源时，relay 以前从 r.TLS 推断 scheme，
// 浏览器发来的 https:// Origin 被判成跨源，HTTPS 部署下 WS 连不上。
func Test反代HTTPS回源时Origin校验通过(t *testing.T) {
	// 这个用例必须校验 Origin，因此要显式设置 host。
	// newRelayServer 传空 host 表示「不校验」，测不到这条路径。
	s, _, _ := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	s.host = srv.Listener.Addr().String()
	// 模拟反代：HTTP 回源 + X-Forwarded-Proto: https。
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = s.host
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://"+s.host)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("反代 HTTPS 回源被误判为跨源: %s", rec.Body.String())
	}
	// 反向边界： Origin 与声明 scheme 不一致时仍必须拒绝。
	bad := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	bad.Host = s.host
	bad.Header.Set("X-Forwarded-Proto", "https")
	bad.Header.Set("Origin", "https://evil.example")
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, bad)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("伪造的跨源 Origin 应被拒绝: %d", rec2.Code)
	}
	// 未声明 X-Forwarded-Proto 时按 http 处理，https Origin 仍被拒绝。
	plain := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	plain.Host = s.host
	plain.Header.Set("Origin", "https://"+s.host)
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, plain)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("无代理头时不应把 http 请求当成 https: %d", rec3.Code)
	}
}
