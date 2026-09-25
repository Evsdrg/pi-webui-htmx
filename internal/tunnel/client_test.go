package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type recordingHandler struct {
	frames atomic.Int64
	last   atomic.Value
}

func (h *recordingHandler) HandleFrame(ctx context.Context, frame []byte) bool {
	h.frames.Add(1)
	h.last.Store(string(frame))
	return true
}

// fakeRelay 是一个最小 relay：接受 /tunnel 与 /client 并互相转发。
// 返回的 kick 用于主动踢掉隧道连接，模拟网络中断。
func fakeRelay(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	var mu sync.Mutex
	tunnels := map[string]*websocket.Conn{}
	clients := map[string][]*websocket.Conn{}
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("deviceId")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		mu.Lock()
		tunnels[id] = conn
		mu.Unlock()
		defer func() { mu.Lock(); delete(tunnels, id); mu.Unlock() }()
		ctx := r.Context()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			for _, c := range clients[id] {
				_ = c.Write(ctx, websocket.MessageText, b)
			}
		}
	})
	mux.HandleFunc("/client", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("deviceId")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		mu.Lock()
		clients[id] = append(clients[id], conn)
		mu.Unlock()
		defer func() {
			mu.Lock()
			list := clients[id]
			for i, c := range list {
				if c == conn {
					clients[id] = append(list[:i], list[i+1:]...)
					break
				}
			}
			mu.Unlock()
		}()
		ctx := r.Context()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			if t, ok := tunnels[id]; ok {
				_ = t.Write(ctx, websocket.MessageText, b)
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	kick := func() {
		mu.Lock()
		conn := tunnels["dev-1"]
		mu.Unlock()
		if conn != nil {
			conn.CloseNow()
		}
	}
	return srv, kick
}

func Test隧道双向收发(t *testing.T) {
	srv, _ := fakeRelay(t)
	handler := &recordingHandler{}
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	cfg.DeviceToken = "tok"
	c := NewClient(cfg, handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case <-c.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("隧道未建立")
	}

	// 浏览器 -> 隧道 -> 处理器。
	client := dialOrFail(t, srv, "/client?deviceId=dev-1")
	defer client.CloseNow()
	payload := `{"kind":"command"}`
	if err := client.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && handler.frames.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if handler.frames.Load() != 1 {
		t.Fatalf("处理器未收到帧: %d", handler.frames.Load())
	}
	if got, _ := handler.last.Load().(string); got != payload {
		t.Fatalf("帧内容不一致: %q", got)
	}

	// 隧道 -> 浏览器。
	reply := `{"kind":"response"}`
	if err := c.Send([]byte(reply)); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	_, got, err := client.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != reply {
		t.Fatalf("回帧不一致: %s", got)
	}
}

func Test未就绪时发送报错(t *testing.T) {
	srv, _ := fakeRelay(t)
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-x"
	c := NewClient(cfg, &recordingHandler{})
	if err := c.Send([]byte("x")); err == nil {
		t.Fatal("未建立隧道时应报错")
	}
	if err := c.Send(nil); err == nil {
		t.Fatal("空帧应报错")
	}
}

func Test断线后自动重连(t *testing.T) {
	srv, kick := fakeRelay(t)
	handler := &recordingHandler{}
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	c := NewClient(cfg, handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case <-c.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("首次连接失败")
	}
	// 主动踢掉隧道连接，客户端应自行重连。
	kick()
	select {
	case <-c.Ready():
	case <-time.After(15 * time.Second):
		t.Fatal("断线后未重连")
	}
	if stats := c.Stats(); stats["connected"] != true {
		t.Fatalf("重连后状态异常: %v", stats)
	}
}

func TestStats字段(t *testing.T) {
	srv, _ := fakeRelay(t)
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	c := NewClient(cfg, &recordingHandler{})
	stats := c.Stats()
	for _, key := range []string{"connected", "queued", "maxQueue", "lastDial"} {
		if _, ok := stats[key]; !ok {
			t.Fatalf("统计缺少 %s: %v", key, stats)
		}
	}
	if c.URL() != cfg.RelayURL || c.DeviceID() != "dev-1" {
		t.Fatal("配置访问器异常")
	}
}

func TestURL转义(t *testing.T) {
	if got := urlQueryEscape("a b&c=d"); got != "a%20b%26c%3Dd" {
		t.Fatalf("转义异常: %q", got)
	}
	if got := urlQueryEscape("dev-1_2.3~x"); got != "dev-1_2.3~x" {
		t.Fatalf("安全字符不应被转义: %q", got)
	}
}

func dialOrFail(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	return conn
}
