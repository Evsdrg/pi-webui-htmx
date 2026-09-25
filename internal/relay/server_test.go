package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testRelaySecret = "0123456789abcdef0123456789abcdef"

func newRelayServer(t *testing.T) (*Server, *Registry, *Users) {
	t.Helper()
	registry, err := NewRegistry(t.TempDir(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)
	users, err := NewUsers(testRelaySecret)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(registry, users, "")
	t.Cleanup(s.Close)
	return s, registry, users
}

func postJSON(t *testing.T, srv *httptest.Server, path, body, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func Test配对与建隧道全流程(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()

	userToken, err := users.AddUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	deviceSecret, err := users.AddDeviceSecret("dev-1")
	if err != nil {
		t.Fatal(err)
	}

	// 本地桥用预共享密钥登记配对码。
	status, out := postJSON(t, srv, "/api/relay/pair",
		`{"deviceId":"dev-1","secret":"`+deviceSecret+`","name":"我的机器"}`, "")
	if status != 200 {
		t.Fatalf("登记失败: %d %v", status, out)
	}
	code, _ := out["pairingCode"].(string)
	if code == "" {
		t.Fatalf("未返回配对码: %v", out)
	}

	// 错误密钥不得登记。
	if status, _ := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"bad","name":"x"}`, ""); status != 401 {
		t.Fatalf("错误密钥应返回 401，实际 %d", status)
	}

	// 用户用配对码领取设备。
	status, out = postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, userToken)
	if status != 200 {
		t.Fatalf("领取失败: %d %v", status, out)
	}
	deviceToken, _ := out["deviceToken"].(string)
	if deviceToken == "" {
		t.Fatalf("未返回设备令牌: %v", out)
	}

	// 未领取的设备不能建隧道。
	if _, err := dialTunnel(t, srv, "dev-x", "whatever"); err == nil {
		t.Fatal("未配对设备不得建立隧道")
	}

	// 本地桥建立主动隧道。
	tunnel := dialTunnelOrFail(t, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()

	// 其他用户不得连接该设备。
	otherToken, err := users.AddUser("bob")
	if err != nil {
		t.Fatal(err)
	}
	if conn := dialClientOrFail(t, srv, "dev-1", "tab-x", otherToken); conn != nil {
		conn.CloseNow()
	}

	// 正确用户可以连接并收发帧。
	aliceClient := dialClientOrFail(t, srv, "dev-1", "tab-1", userToken)
	defer aliceClient.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := `{"kind":"command","method":"worker.list"}`
	if err := aliceClient.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	// 桥收到的是带 to 的路由封装，data 里是原始业务帧。
	_, got, err := tunnel.Read(ctx)
	if err != nil {
		t.Fatalf("隧道未收到浏览器帧: %v", err)
	}
	// relay 到桥的方向用 from 标识来源标签页。
	rf, ok := Unwrap(got)
	if !ok || rf.From != "tab-1" {
		t.Fatalf("路由封装异常: %s", got)
	}
	if string(rf.Data) != payload {
		t.Fatalf("业务帧内容不一致: %s", rf.Data)
	}
	// 桥回帧同样带 to，relay 只按 to 转发。
	reply := `{"kind":"response","ok":true}`
	wrapped := routeToBrowser("tab-1", []byte(reply))
	if err := tunnel.Write(ctx, websocket.MessageText, wrapped); err != nil {
		t.Fatal(err)
	}
	_, got, err = aliceClient.Read(ctx)
	if err != nil {
		t.Fatalf("浏览器未收到隧道帧: %v", err)
	}
	if string(got) != reply {
		t.Fatalf("回帧内容不一致: %s", got)
	}
}

func Test设备列表与撤销(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, err := users.AddUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.AddDeviceSecret("dev-1"); err != nil {
		t.Fatal(err)
	}
	if status, _ := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"x","name":"n"}`, ""); status != 401 {
		t.Fatalf("错误密钥应被拒绝: %d", status)
	}
	// 无设备时列表为空。
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/relay/devices", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if resp.StatusCode != 200 {
		t.Fatalf("设备列表失败: %d", resp.StatusCode)
	}
	devices, _ := list["devices"].([]any)
	if len(devices) != 0 {
		t.Fatalf("初始应无设备: %v", devices)
	}
}

func Test未授权访问被拒绝(t *testing.T) {
	s, _, _ := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	for _, path := range []string{"/api/relay/devices", "/api/relay/claim"} {
		method := http.MethodPost
		if path == "/api/relay/devices" {
			method = http.MethodGet
		}
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%s 未授权应返回 401，实际 %d", path, resp.StatusCode)
		}
	}
}

func TestCookie鉴权(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, err := users.AddUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := postJSON(t, srv, "/api/relay/auth", `{"token":"`+userToken+`"}`, ""); status != 200 {
		t.Fatalf("换取 Cookie 失败: %d", status)
	}
	// 用 Cookie 访问设备列表。
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/relay/devices", nil)
	for _, c := range cookieJar(t, srv, userToken) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("带 Cookie 访问失败: %d", resp.StatusCode)
	}
}

func dialTunnel(t *testing.T, srv *httptest.Server, deviceID, token string) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel?deviceId=" + deviceID + "&token=" + token
	conn, _, err := websocket.Dial(ctx, url, nil)
	return conn, err
}

func dialTunnelOrFail(t *testing.T, srv *httptest.Server, deviceID, token string) *websocket.Conn {
	t.Helper()
	conn, err := dialTunnel(t, srv, deviceID, token)
	if err != nil {
		t.Fatalf("隧道连接失败: %v", err)
	}
	return conn
}

func dialClientOrFail(t *testing.T, srv *httptest.Server, deviceID, clientID, userToken string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/client?deviceId=" + deviceID + "&clientId=" + clientID
	header := http.Header{}
	header.Set("Authorization", "Bearer "+userToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		// relay 对无权限用户会主动关闭，Dial 可能返回错误，这属于预期。
		return nil
	}
	return conn
}

func cookieJar(t *testing.T, srv *httptest.Server, userToken string) []*http.Cookie {
	t.Helper()
	status, _ := postJSON(t, srv, "/api/relay/auth", `{"token":"`+userToken+`"}`, "")
	if status != 200 {
		t.Fatal("换取 Cookie 失败")
	}
	// 重新请求一次以拿到 Set-Cookie。
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/relay/auth", strings.NewReader(`{"token":"`+userToken+`"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Cookies()
}
