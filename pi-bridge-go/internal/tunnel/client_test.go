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

	"pi-bridge-go/internal/testutil"
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
		// 先等连接真正注册进 relay 再关。客户端 Dial 成功 ≠ 服务端 handler
		// 已执行到 tunnels[id] = conn——两者之间有个窗口，而测试是在客户端
		// Ready 之后立刻 kick 的：踢到空就什么都不会发生，客户端当然不重连
		// （表现为 15 秒超时，偶发）。
		testutil.WaitFor(t, "隧道连接注册到 relay", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return tunnels["dev-1"] != nil
		})
		mu.Lock()
		conn := tunnels["dev-1"]
		mu.Unlock()
		conn.CloseNow()
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
	// 预算 15 秒而不是 5 秒：CI 上 -race 加持、十几个包并行时，握手会被
	// 调度拖慢。断言不变，只放宽预算——短预算会把「机器慢」报成「隧道没建立」。
	select {
	case <-c.Ready():
	case <-time.After(15 * time.Second):
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
	case <-time.After(15 * time.Second):
		t.Fatal("首次连接失败")
	}
	// 用「连接代次」判定重连，而不是再取一次 Ready()：踢掉之后 Ready()
	// 会短暂变为未就绪、连上后又就绪，两次就绪之间隔多久取决于调度，
	// 按通道比较会因为看得早晚而漏判（这条测试曾以 15 秒超时间歇失败）。
	// 代次是单调递增的，不存在这个窗口。
	if _, ok := c.Stats()["connects"]; !ok {
		t.Fatalf("Stats 缺少 connects: %v", c.Stats())
	}
	before := statInt(c, "connects")
	// 主动踢掉隧道连接，客户端应自行重连。
	kick()
	testutil.WaitFor(t, "断线后重连", func() bool { return statInt(c, "connects") > before })
	if stats := c.Stats(); stats["connected"] != true {
		t.Fatalf("重连后状态异常: %v", stats)
	}
}

// 每次成功连上后退避必须回落：否则 relay 重启后桥会等满 30 秒才重连。
// 连续踢 6 次，每次都要在很短时间内重连；退避不回落时第 6 次早已涨到
// 30 秒上限，整轮必然超时。
func Test重连退避在连上后回落(t *testing.T) {
	srv, kick := fakeRelay(t)
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	c := NewClient(cfg, &recordingHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case <-c.Ready():
	case <-time.After(15 * time.Second):
		t.Fatal("首次连接失败")
	}
	deadline := time.Now().Add(15 * time.Second)
	for i := 0; i < 6; i++ {
		before := statInt(c, "connects")
		kick()
		for statInt(c, "connects") <= before {
			if time.Now().After(deadline) {
				t.Fatalf("第 %d 次重连退避未回落（连上后应重置为 1 秒）", i+1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestStats字段(t *testing.T) {
	srv, _ := fakeRelay(t)
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	c := NewClient(cfg, &recordingHandler{})
	stats := c.Stats()
	for _, key := range []string{"connected", "queued", "maxQueue", "connects", "lastDial"} {
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

func statInt(c *Client, key string) int64 {
	n, _ := c.Stats()[key].(int64)
	return n
}

// 回归：Ready() 表达的是「当前是否连通」，而不是一次「连上了」的事件。
//
// 旧实现里，连接已经建立之后再取 Ready()，拿到的是「下一次重连」的通道，
// 会一直等下去。CI 上 Test隧道双向收发 因此以 5 秒超时间歇失败，而本地
// 几乎总是先订阅、看不出问题。
func Test已连接后取Ready仍就绪(t *testing.T) {
	srv, _ := fakeRelay(t)
	cfg := Defaults()
	cfg.RelayURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg.DeviceID = "dev-1"
	c := NewClient(cfg, &recordingHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	select {
	case <-c.Ready():
	case <-time.After(15 * time.Second):
		t.Fatal("首次连接失败")
	}
	// 等「连接建立」这个时刻彻底过去。这里不是规避时序：连接已建立且
	// 无人断开，状态是稳定的，sleep 只是确保订阅发生在事件之后。
	time.Sleep(200 * time.Millisecond)
	select {
	case <-c.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("已连接，但订阅晚一步的 Ready() 未就绪")
	}
}
