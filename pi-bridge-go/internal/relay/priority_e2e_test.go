package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// B53 端到端：慢消费者 + 满预算时，控制帧应当等预算归还，
// 而不是被当成「超预算」直接断连接。
//
// 旧实现的放行线是 max(预算, 本帧大小)：队列已接近预算时，
// 哪怕只有几十字节的控制消息也会顶出上限，整条连接被踢掉——
// 一个只是暂时读得慢的浏览器会因为一条状态消息而断开。
func Test满预算时控制帧等待而不是断连(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	userToken, _ := users.AddUser("alice")
	deviceSecret, _ := users.AddDeviceSecret("dev-1")
	_, out := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"`+deviceSecret+`"}`, "")
	code, _ := out["pairingCode"].(string)
	_, out2 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, userToken)
	deviceToken, _ := out2["deviceToken"].(string)

	tunnel := dialTunnelOrFail(t, s, srv, "dev-1", deviceToken)
	defer tunnel.CloseNow()
	client := dialClientOrFail(t, srv, "dev-1", "tab-1", userToken)
	defer client.CloseNow()
	client.SetReadLimit(1 << 20)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 灌到「内核缓冲 + 预算」都满：64 × 256 KiB = 16 MiB。
	// 只写 4 MiB 是测不出差异的——pumpWrites 会把帧直接写进内核缓冲，
	// 通道里的 queued 一直是低位，预算根本没被占满。
	blob := strings.Repeat("x", 256<<10)
	const contents = 64
	written := make(chan error, 1)
	go func() {
		for i := 0; i < contents; i++ {
			payload, _ := json.Marshal(map[string]any{"kind": "content", "index": i, "blob": blob})
			if err := tunnel.Write(ctx, websocket.MessageText, routeToBrowser("tab-1", payload)); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	// 等内核缓冲与队列表都堆起来（写侧此时会被背压挡住）。
	time.Sleep(600 * time.Millisecond)

	// 控制帧：预算已满，它必须等归还而不是断开连接。
	control := []byte(`{"kind":"subscription_closed","reason":"idle"}`)
	controlSent := make(chan error, 1)
	go func() { controlSent <- tunnel.Write(ctx, websocket.MessageText, routeToBrowser("tab-1", control)) }()
	time.Sleep(200 * time.Millisecond)

	// 浏览器开始读：预算归还后控制帧应当送达，连接保持可用。
	deadline := time.Now().Add(20 * time.Second)
	gotControl := false
	for i := 0; time.Now().Before(deadline) && !gotControl && i < contents+8; i++ {
		_, frame, err := client.Read(ctx)
		if err != nil {
			t.Fatalf("连接在控制帧送达前断开（旧实现会这样）：%v", err)
		}
		if bytes.Contains(frame, control) {
			gotControl = true
		}
	}
	if !gotControl {
		t.Fatal("预算归还后控制帧仍未送达")
	}
	if err := <-controlSent; err != nil {
		t.Fatalf("控制帧写入失败: %v", err)
	}
}
