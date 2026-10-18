package transport

import (
	"bytes"
	"context"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

// 出站队列在暂满时应当等待消费者归还预算，而不是断开连接。
// WebSocket 连接与隧道虚拟连接共用同一份 outboundQueue，两者都要成立。
func Test出站队列暂满时等待消费者而不关闭连接(t *testing.T) {
	frame := protocol.Message{Version: 1, Kind: "event", Event: "pi.event", Data: map[string]string{"text": "hello"}}
	for _, tc := range []struct {
		name     string
		capacity int
		make     func(context.Context, context.CancelFunc, chan []byte) interface {
			send(protocol.Message) bool
		}
	}{
		{"ws", 32, func(ctx context.Context, cancel context.CancelFunc, out chan []byte) interface {
			send(protocol.Message) bool
		} {
			return &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
		}},
		{"tunnel", 64, func(ctx context.Context, cancel context.CancelFunc, out chan []byte) interface {
			send(protocol.Message) bool
		} {
			return &virtualConn{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out := make(chan []byte, tc.capacity)
			sink := tc.make(ctx, cancel, out)
			for i := 0; i < tc.capacity; i++ {
				if !sink.send(frame) {
					t.Fatal("填入有界队列失败")
				}
			}
			result := make(chan bool, 1)
			go func() { result <- sink.send(frame) }()
			select {
			case ok := <-result:
				t.Fatalf("消费者尚未读取时提前返回: %v", ok)
			case <-time.After(20 * time.Millisecond):
			}
			// writer/pump 读取后，排队的帧应该按原顺序继续送出。
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
				t.Fatal("未在消费者取帧后继续")
			}
		})
	}
}

func Test出站字节预算暂满时等待消费者(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := make(chan []byte, 32)
	c := &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
	// 单帧 480 KiB：两帧即逼近 1 MiB 预算，第三帧必须等待归还。
	frame := protocol.Message{Version: 1, Kind: "event", Event: "pi.event", Data: map[string]string{"text": string(bytes.Repeat([]byte("x"), 480<<10))}}
	for i := 0; i < 2; i++ {
		if !c.send(frame) {
			t.Fatal("初始帧入队失败")
		}
	}
	result := make(chan bool, 1)
	go func() { result <- c.send(frame) }()
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
		t.Fatal("字节预算释放后未继续")
	}
}

// 消费者持续不读取时，投递必须按 outboundWait 退出，且不泄漏排队预算、
// 不取消连接（连接可用性由上层判断，队列等待本身不该断连）。
func Test出站等待超时后连接仍可用(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan []byte, 1)
	c := &connection{ctx: ctx, cancel: cancel, queue: &outboundQueue{frames: out, space: make(chan struct{}, 1)}}
	first := protocol.Message{Version: 1, Kind: "event", Event: "pi.event", Data: map[string]string{"text": "a"}}
	if !c.send(first) {
		t.Fatal("第一帧入队失败")
	}
	used := c.queue.bytes()
	// 模拟「这次投递的时间预算很短」：直接调 enqueue 带一个短 ctx。
	blocked, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stop()
	payload := []byte(`{"version":1,"kind":"event"}`)
	if c.queue.enqueue(blocked, payload) {
		t.Fatal("消费者持续不读取时必须退出这次投递")
	}
	if ctx.Err() != nil || c.queue.bytes() != used {
		t.Fatalf("投递超时不应取消连接或泄漏排队预算: err=%v bytes=%d used=%d", ctx.Err(), c.queue.bytes(), used)
	}
	drained := <-out
	c.queue.release(int64(len(drained)))
	if !c.send(protocol.Reply("after", map[string]bool{"ok": true}, nil)) {
		t.Fatal("超时后同一连接应可继续发送")
	}
}
