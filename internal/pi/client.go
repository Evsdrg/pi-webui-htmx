// Package pi 负责与 Pi 的 stdio 协议对话，不负责进程调度。
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"pi-bridge-go/internal/jsonl"
)

// ErrClosed 表示与 Pi 的 RPC 连接已关闭。
var ErrClosed = errors.New("Pi RPC 连接已关闭")

// ErrLimit 表示在途命令数量达到上限。
var ErrLimit = errors.New("Pi RPC 在途命令数量已达上限")

// UnknownOutcome 表示命令可能已被 Pi 收到，但结果未知，调用方不得自动重试。
type UnknownOutcome struct{ Cause error }

func (e *UnknownOutcome) Error() string { return "Pi 可能已收到该命令：" + e.Cause.Error() }
func (e *UnknownOutcome) Unwrap() error { return e.Cause }

// RPCError 表示 Pi 明确返回的失败。
type RPCError struct{ Message string }

func (e *RPCError) Error() string { return e.Message }

// result 是一条命令的等待结果。
type result struct {
	data json.RawMessage
	err  error
}

// writeItem 是排队写入 stdin 的帧。
type writeItem struct {
	ctx  context.Context
	data []byte
	ack  chan error
}

// Client 持有与单个 Pi 进程的 stdin/stdout 连接。
type Client struct {
	in       io.WriteCloser
	out      io.ReadCloser
	maxFrame int
	onEvent  func(json.RawMessage)
	mu       sync.Mutex
	pending  map[string]chan result
	next     atomic.Uint64
	writes   chan writeItem
	done     chan struct{}
	once     sync.Once
}

func New(in io.WriteCloser, out io.ReadCloser, maxFrame int, onEvent func(json.RawMessage)) *Client {
	return &Client{in: in, out: out, maxFrame: maxFrame, onEvent: onEvent, pending: make(map[string]chan result), writes: make(chan writeItem, 64), done: make(chan struct{})}
}

func (c *Client) Start()                { go c.read(); go c.write() }
func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) Close()                { c.fail(ErrClosed) }
func (c *Client) CloseInput()           { _ = c.in.Close() }

// fail 关闭连接并唤醒所有等待者，保证没有调用方被永久挂起。
func (c *Client) fail(err error) {
	c.once.Do(func() {
		close(c.done)
		_ = c.in.Close()
		_ = c.out.Close()
		c.mu.Lock()
		defer c.mu.Unlock()
		for id, ch := range c.pending {
			ch <- result{err: err}
			delete(c.pending, id)
		}
	})
}

// Call 发送一条命令并等待响应。ctx 只约束等待时长，不会取消浏览器任务。
func (c *Client) Call(ctx context.Context, typ string, fields map[string]any) (json.RawMessage, error) {
	id := fmt.Sprintf("rpc-%d", c.next.Add(1))
	cmd := map[string]any{"type": typ, "id": id}
	for k, v := range fields {
		if k != "id" && k != "type" {
			cmd[k] = v
		}
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if len(b) > c.maxFrame {
		return nil, jsonl.ErrTooLarge
	}
	ch := make(chan result, 1)
	c.mu.Lock()
	if len(c.pending) >= 64 {
		c.mu.Unlock()
		return nil, ErrLimit
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, ErrClosed
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	item := writeItem{ctx: ctx, data: b, ack: make(chan error, 1)}
	select {
	case c.writes <- item:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
	// 已入队后，取消与写盘之间会竞态，结果一律按未知处理。
	select {
	case err = <-item.ack:
		if err != nil {
			return nil, &UnknownOutcome{Cause: err}
		}
	case <-ctx.Done():
		return nil, &UnknownOutcome{Cause: ctx.Err()}
	case <-c.done:
		return nil, &UnknownOutcome{Cause: ErrClosed}
	}
	select {
	case r := <-ch:
		if r.err != nil {
			var re *RPCError
			if !errors.As(r.err, &re) {
				return nil, &UnknownOutcome{Cause: r.err}
			}
		}
		return r.data, r.err
	case <-ctx.Done():
		return nil, &UnknownOutcome{Cause: ctx.Err()}
	case <-c.done:
		select {
		case r := <-ch:
			if r.err == nil {
				return r.data, nil
			}
		default:
		}
		return nil, &UnknownOutcome{Cause: ErrClosed}
	}
}

// Notify 只用于无需响应的扩展 UI 回执，不用于命令，且永不阻塞 stdout 读取。
// 队列已满时判定连接异常并关闭，避免无界堆积。
func (c *Client) Notify(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.fail(err)
		return
	}
	b = append(b, '\n')
	if len(b) > c.maxFrame {
		c.fail(jsonl.ErrTooLarge)
		return
	}
	select {
	case c.writes <- writeItem{ctx: context.Background(), data: b, ack: make(chan error, 1)}:
	case <-c.done:
	default:
		c.fail(ErrLimit)
	}
}

// write 串行化所有 stdin 写入；发送下一条命令不等待上一条完成，否则长命令会挡住取消。
func (c *Client) write() {
	for {
		select {
		case <-c.done:
			return
		case w := <-c.writes:
			if err := w.ctx.Err(); err != nil {
				w.ack <- err
				continue
			}
			deadline := time.Now().Add(5 * time.Second)
			if d, ok := w.ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
			if p, ok := c.in.(interface{ SetWriteDeadline(time.Time) error }); ok {
				_ = p.SetWriteDeadline(deadline)
			}
			n, err := c.in.Write(w.data)
			if err == nil && n != len(w.data) {
				err = io.ErrShortWrite
			}
			w.ack <- err
			if err != nil {
				c.fail(err)
				return
			}
		}
	}
}

// read 持续消费 stdout；必须始终读取，否则 Pi 会因背压停止输出。
func (c *Client) read() {
	r := bufio.NewReader(c.out)
	for {
		b, _, err := jsonl.Read(r, c.maxFrame)
		if err != nil {
			c.fail(err)
			return
		}
		var frame struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
			Error   string          `json:"error"`
		}
		if err = json.Unmarshal(b, &frame); err != nil || frame.Type == "" {
			c.fail(errors.New("收到非法的 Pi RPC 帧"))
			return
		}
		if frame.Type == "response" {
			rr := result{data: frame.Data}
			if !frame.Success {
				rr.err = &RPCError{Message: frame.Error}
			}
			c.mu.Lock()
			if ch, ok := c.pending[frame.ID]; ok {
				delete(c.pending, frame.ID)
				ch <- rr
			}
			c.mu.Unlock()
		} else if c.onEvent != nil {
			c.onEvent(json.RawMessage(b))
		}
	}
}
