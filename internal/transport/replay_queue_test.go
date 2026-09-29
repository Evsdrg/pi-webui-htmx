package transport

import (
	"bytes"
	"context"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

func Test补发队列暂满时等待消费者而不关闭连接(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capacity int
		make     func(context.Context, context.CancelFunc, chan []byte) interface {
			sendRaw(context.Context, []byte) bool
		}
	}{
		{"ws", 32, func(ctx context.Context, cancel context.CancelFunc, out chan []byte) interface {
			sendRaw(context.Context, []byte) bool
		} {
			return &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
		}},
		{"tunnel", 64, func(ctx context.Context, cancel context.CancelFunc, out chan []byte) interface {
			sendRaw(context.Context, []byte) bool
		} {
			return &virtualConn{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out := make(chan []byte, tc.capacity)
			sink := tc.make(ctx, cancel, out)
			frame := []byte(`{"version":1,"kind":"event"}`)
			for i := 0; i < tc.capacity; i++ {
				if !sink.sendRaw(ctx, frame) {
					t.Fatal("填入有界队列失败")
				}
			}
			result := make(chan bool, 1)
			go func() { result <- sink.sendRaw(ctx, frame) }()
			select {
			case ok := <-result:
				t.Fatalf("消费者尚未读取时补发提前返回: %v", ok)
			case <-time.After(20 * time.Millisecond):
			}
			// writer/pump 读取后，排队的补发帧应该按原顺序继续送出。
			first := <-out
			switch c := sink.(type) {
			case *connection:
				c.queue.release(int64(len(first)))
			case *virtualConn:
				c.queue.release(int64(len(first)))
			}
			select {
			case ok := <-result:
				if !ok || ctx.Err() != nil {
					t.Fatalf("队列恢复后仍取消连接: ok=%v err=%v", ok, ctx.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("补发未在消费者取帧后继续")
			}
		})
	}
}

func Test补发字节预算暂满时等待消费者(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := make(chan []byte, 32)
	c := &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
	frame := bytes.Repeat([]byte("x"), 480<<10)
	for i := 0; i < 2; i++ {
		if !c.sendRaw(ctx, frame) {
			t.Fatal("初始帧入队失败")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- c.sendRaw(ctx, frame) }()
	select {
	case ok := <-result:
		t.Fatalf("字节预算暂满却提前退出: %v", ok)
	case <-time.After(20 * time.Millisecond):
	}
	first := <-out
	c.queue.release(int64(len(first)))
	select {
	case ok := <-result:
		if !ok || ctx.Err() != nil {
			t.Fatalf("字节预算恢复后仍取消连接: ok=%v err=%v", ok, ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("字节预算释放后补发未继续")
	}
}

func Test补发超时不会让连接陷入重连循环(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan []byte, 1)
	c := &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
	frame := []byte(`{"version":1,"kind":"event"}`)
	if !c.sendRaw(ctx, frame) {
		t.Fatal("第一帧入队失败")
	}
	replayCtx, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	if c.sendRaw(replayCtx, frame) {
		t.Fatal("消费者持续不读取时必须退出这次补发")
	}
	if ctx.Err() != nil || c.queue.bytes() != int64(len(frame)) {
		t.Fatalf("补发超时不应取消连接或泄漏排队预算: err=%v bytes=%d", ctx.Err(), c.queue.bytes())
	}
	first := <-out
	c.queue.release(int64(len(first)))
	if !c.send(protocol.Reply("resync", map[string]bool{"ok": true}, nil)) {
		t.Fatal("补发超时后同一连接应可发送重同步响应")
	}
}

func Test实时事件短暂拥塞不立即断开(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan []byte, 32)
	c := &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
	event := protocol.Message{Version: 1, Kind: "event", Event: "pi.event", Data: map[string]string{"text": "hello"}}
	for i := 0; i < cap(out); i++ {
		if !c.send(event) {
			t.Fatal("初始事件入队失败")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- c.send(event) }()
	select {
	case ok := <-result:
		t.Fatalf("消费者尚未读取时实时帧提前返回: %v", ok)
	case <-time.After(20 * time.Millisecond):
	}
	first := <-out
	c.queue.release(int64(len(first)))
	select {
	case ok := <-result:
		if !ok || ctx.Err() != nil {
			t.Fatalf("实时帧恢复后仍取消连接: ok=%v err=%v", ok, ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("实时帧未在消费者取帧后继续")
	}
}
