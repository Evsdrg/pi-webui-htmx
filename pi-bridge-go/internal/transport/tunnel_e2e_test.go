package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/relay"
	"pi-bridge-go/internal/tunnel"
)

// B54 端到端：浏览器 ↔ relay（设备前缀 WS）↔ 隧道 ↔ 桥的虚拟连接。
//
// 这条链路的每一段都已单独测过，但它们合起来是否成立只有真跑一遍才知道：
// 帧要从浏览器的 WS 进 relay、包上来源标识、经隧道到桥、被桥当命令处理，
// 回帧再沿原路回到同一个浏览器连接。
func Test经relay与隧道的浏览器连接(t *testing.T) {
	// relay：Host 用不含端口的形式，匹配时忽略请求端口。
	registry, err := relay.NewRegistry(t.TempDir(), relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)
	users, err := relay.NewUsers("0123456789abcdef0123456789abcdef", "")
	if err != nil {
		t.Fatal(err)
	}
	relayServer, err := relay.NewServer(registry, users, relay.Config{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relayServer.Close)
	srv := httptest.NewServer(relayServer)
	defer srv.Close()

	userToken, err := users.AddUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	code, err := registry.Register("dev-1", "测试设备")
	if err != nil {
		t.Fatal(err)
	}
	_, deviceToken, err := registry.Claim("alice", code)
	if err != nil {
		t.Fatal(err)
	}

	// 桥：真 Server + 真 TunnelBridge + 真隧道客户端。
	server, _, _ := newTestServer(t)
	bridge := NewTunnelBridge(server, nil, 8, 5*time.Minute)
	server.SetTunnelBridge(bridge)
	cfg := tunnel.Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	cfg.DeviceToken = deviceToken
	client := tunnel.NewClient(cfg, bridge)
	bridge.SetSender(client.Send)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	// 等隧道注册（放宽到 20 秒：首次 dial 失败会走退避）。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if online, _ := relayServer.Stats()["tunnels"].(int); online == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if online, _ := relayServer.Stats()["tunnels"].(int); online != 1 {
		t.Fatalf("隧道未注册，relay 状态: %+v，注册表: %+v", relayServer.Stats(), registry.Stats())
	}

	// 浏览器：设备前缀下的 WS，用 relay 的用户令牌认证。
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/d/dev-1/api/v1/ws"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+userToken)
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("浏览器侧 WS 应能建立: %v", err)
	}
	defer conn.CloseNow()

	// 发一条桥能在无 worker 下处理的命令。
	if err := conn.Write(dialCtx, websocket.MessageText, []byte(`{"version":1,"kind":"command","requestId":"c1","method":"worker.list"}`)); err != nil {
		t.Fatal(err)
	}
	_, frame, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatalf("未收到桥的响应（整条链路未打通）: %v", err)
	}
	var reply map[string]any
	if err := json.Unmarshal(frame, &reply); err != nil {
		t.Fatalf("响应不是 JSON: %s", frame)
	}
	if reply["requestId"] != "c1" || reply["ok"] != true {
		t.Fatalf("响应内容不对: %s", frame)
	}
}
