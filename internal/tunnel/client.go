// Package tunnel 让本地桥主动外连到云端 relay。
// 本地不需要任何入站端口；断线由 bridge 侧重连，relay 不保存会话正文。
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ErrNotReady 表示隧道尚未建立。
var ErrNotReady = errors.New("隧道尚未建立")

// Config 是隧道客户端配置。
type Config struct {
	RelayURL    string // 例如 wss://relay.example.com
	DeviceID    string // 设备标识
	DeviceToken string // 设备令牌，claim 时获得
	DialTimeout time.Duration
	QueueBytes  int64
	QueueMsgs   int
}

// Defaults 给出默认配置。
func Defaults() Config {
	return Config{DialTimeout: 15 * time.Second, QueueBytes: 2 << 20, QueueMsgs: 64}
}

// Client 维护一条到 relay 的主动隧道。
type Client struct {
	cfg     Config
	handler Handler

	mu      sync.Mutex
	conn    *websocket.Conn
	cancel  context.CancelFunc
	out     chan []byte
	queued  atomic.Int64
	ready   chan struct{}
	readyAt time.Time
}

// Handler 处理从 relay 收到的浏览器帧。
type Handler interface {
	// HandleFrame 处理一条浏览器发来的帧，返回 true 表示已消费。
	HandleFrame(ctx context.Context, frame []byte) bool
}

// NewClient 构造隧道客户端。
func NewClient(cfg Config, handler Handler) *Client {
	if cfg.QueueBytes <= 0 {
		cfg.QueueBytes = 2 << 20
	}
	if cfg.QueueMsgs <= 0 {
		cfg.QueueMsgs = 64
	}
	return &Client{cfg: cfg, handler: handler, out: make(chan []byte, cfg.QueueMsgs), ready: make(chan struct{})}
}

// URL 返回隧道地址。
func (c *Client) URL() string { return c.cfg.RelayURL }

// DeviceID 返回设备标识。
func (c *Client) DeviceID() string { return c.cfg.DeviceID }

// Run 连接并保持隧道，阻塞直到 ctx 取消。
// 断线后按退避重连；永不因单次失败退出。
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	maxBackoff := 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.dialAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// 退避重连，避免 relay 故障时疯狂重试。
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		backoff = time.Second
	}
}

func (c *Client) dialAndServe(ctx context.Context) error {
	dialctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()
	url := strings.TrimSuffix(c.cfg.RelayURL, "/") + "/tunnel?deviceId=" +
		urlQueryEscape(c.cfg.DeviceID) + "&token=" + urlQueryEscape(c.cfg.DeviceToken)
	conn, _, err := websocket.Dial(dialctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{"pi-bridge-tunnel/1"}},
	})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxFrame)

	connCtx, connCancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.conn = conn
	c.cancel = connCancel
	c.queued.Store(0)
	// 清空上一轮的残留帧，避免把旧连接的数据发给新连接。
	drain(c.out)
	c.readyAt = time.Now()
	c.mu.Unlock()
	c.signalReady()
	defer func() {
		connCancel()
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.cancel = nil
		}
		c.mu.Unlock()
	}()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-connCtx.Done():
				return
			case b := <-c.out:
				c.queued.Add(-int64(len(b)))
				wctx, wcancel := context.WithTimeout(connCtx, writeTimeout)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()
	for {
		typ, b, err := conn.Read(connCtx)
		if err != nil {
			connCancel()
			<-writeDone
			return err
		}
		if typ != websocket.MessageText && typ != websocket.MessageBinary {
			continue
		}
		frame := make([]byte, len(b))
		copy(frame, b)
		if !c.handler.HandleFrame(connCtx, frame) {
			// 处理器无法消费时丢弃，不在桥内堆积。
			continue
		}
	}
}

// Send 把一帧发给浏览器侧；队列满时丢弃并返回错误。
func (c *Client) Send(frame []byte) error {
	if len(frame) == 0 {
		return errors.New("空帧")
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotReady
	}
	if c.queued.Add(int64(len(frame))) > c.cfg.QueueBytes {
		c.queued.Add(-int64(len(frame)))
		return errors.New("隧道发送队列已满")
	}
	select {
	case c.out <- frame:
		return nil
	default:
		c.queued.Add(-int64(len(frame)))
		return errors.New("隧道发送队列已满")
	}
}

// Ready 返回当前连通等待通道；每次（重）连成功时关闭并替换。
// 必须在锁内读取，否则与 signalReady 的替换构成数据竞争。
func (c *Client) Ready() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *Client) signalReady() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.ready:
		// 已被关闭过，直接换新的等待通道。
	default:
		close(c.ready)
	}
	c.ready = make(chan struct{})
}

// Stats 返回隧道状态。
func (c *Client) Stats() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"connected": c.conn != nil,
		"queued":    c.queued.Load(),
		"maxQueue":  c.cfg.QueueBytes,
		"lastDial":  c.readyAt.UTC().Format(time.RFC3339Nano),
	}
}

func drain(ch chan []byte) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func urlQueryEscape(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			const hexDigits = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

const (
	maxFrame     = 1 << 20
	writeTimeout = 10 * time.Second
)

var _ = json.Marshal
