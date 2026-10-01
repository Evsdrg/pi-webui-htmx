package relay

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/testutil"
)

// Test单用户连接数有上限 覆盖 B23：
// /client 可为同一 owner 的任意 clientId 建连，没有上限时
// 一个认证令牌就能耗尽 relay 的 goroutine 与内存。
func Test单用户连接数有上限(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	ut, _ := users.AddUser("alice")
	ds, _ := users.AddDeviceSecret("dev-1")
	_, out := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"`+ds+`"}`, "")
	code, _ := out["pairingCode"].(string)
	_, out2 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, ut)
	dt, _ := out2["deviceToken"].(string)
	dialTunnelOrFail(t, s, srv, "dev-1", dt)

	conns := []*websocket.Conn{}
	defer func() {
		for _, c := range conns {
			c.CloseNow()
		}
	}()
	// 一路建连直到被拒。每个连接用不同 clientId，否则同 clientId 重连
	// 会顶掉旧连接，计数永远停在 1，测不到上限。
	//
	// 判定「被拒」要看计数有没有增长：relay 是先完成 WS 升级再用
	// StatusTryAgainLater 关闭，因此 Dial 对被拒连接同样返回成功。
	accepted := func() bool {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if s.Stats()["clients"].(int) > len(conns) {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	for i := 0; i < maxClientsPerOwner+8; i++ {
		c := dialClientOrFail(t, srv, "dev-1", "tab-"+strconv.Itoa(i), ut)
		if c == nil {
			break
		}
		if !accepted() {
			c.CloseNow()
			break
		}
		conns = append(conns, c)
	}
	if len(conns) >= maxClientsPerOwner+8 {
		t.Fatalf("单用户连接数没有上限：建了 %d 个连接全部成功", len(conns))
	}
	if len(conns) > maxClientsPerOwner {
		t.Fatalf("超出上限仍被接受: %d > %d", len(conns), maxClientsPerOwner)
	}
	if len(conns) == 0 {
		t.Fatal("一个连接都没建成，测试没有覆盖目标路径")
	}
	// 另一台设备的连接不应被本用户的上限影响。
	ds2, _ := users.AddDeviceSecret("dev-2")
	_, out3 := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-2","secret":"`+ds2+`"}`, "")
	code2, _ := out3["pairingCode"].(string)
	_, out4 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code2+`"}`, ut)
	dt2, _ := out4["deviceToken"].(string)
	dialTunnelOrFail(t, s, srv, "dev-2", dt2)
	other := dialClientOrFail(t, srv, "dev-2", "tab-other", ut)
	if other == nil {
		t.Fatal("另一台设备的连接被误判为超限")
	}
	defer other.CloseNow()
}

// Test关闭relay会中断浏览器连接 覆盖 B41：
// Close() 以前只关 tunnels，浏览器 WS 一直挂到对端超时。
func Test关闭relay会中断浏览器连接(t *testing.T) {
	s, _, users := newRelayServer(t)
	srv := httptest.NewServer(s)
	defer srv.Close()
	ut, _ := users.AddUser("alice")
	ds, _ := users.AddDeviceSecret("dev-1")
	_, out := postJSON(t, srv, "/api/relay/pair", `{"deviceId":"dev-1","secret":"`+ds+`"}`, "")
	code, _ := out["pairingCode"].(string)
	_, out2 := postJSON(t, srv, "/api/relay/claim", `{"pairingCode":"`+code+`"}`, ut)
	dt, _ := out2["deviceToken"].(string)
	dialTunnelOrFail(t, s, srv, "dev-1", dt)

	client := dialClientOrFail(t, srv, "dev-1", "tab-1", ut)
	if client == nil {
		t.Fatal("浏览器连接失败")
	}
	defer client.CloseNow()
	testutil.WaitFor(t, "浏览器连接注册", func() bool { return s.Stats()["clients"].(int) == 1 })

	s.Close()
	// 连接必须被服务端主动关闭，而不是留给对端超时。
	testutil.WaitFor(t, "浏览器连接被服务端关闭", func() bool { return s.Stats()["clients"].(int) == 0 })
	// 必须明确读到「连接已关闭」。原写法是「读失败即返回」，
	// 那样即使 Close 什么都没做，也会因为读超时而误判为通过。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := client.Read(ctx)
	if err == nil {
		t.Fatal("Close() 之后浏览器连接仍然可读，说明没有被关闭")
	}
	if !isClosedError(err) {
		t.Fatalf("连接不是被服务端正常关闭的: %v", err)
	}
}

// isClosedError 判断错误是否来自连接被关闭，而不是读超时。
// 超时说明对端还挂着——那正是 B41 的缺陷形态。
func isClosedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "closed") || strings.Contains(msg, "close") ||
		strings.Contains(msg, "EOF") || strings.Contains(msg, "reset")
}
