package relay

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	// maxQueueBytes 是单连接「等待写出」字节的固定预算。
	//
	// 它**不随单帧大小放宽**：以前放行线取 max(预算, 本帧大小)，
	// 于是一个大帧可以把自己和队列里的其它帧一起顶到预算之外（B53）。
	maxQueueBytes = 4 << 20

	// controlFrameMax 是「小帧」的上界，小帧走高优先通道。
	// 只看长度、不看内容——relay 是 routing-only 转发器，不解析载荷（#276）。
	// 订阅确认、错误、状态这类控制消息都在这个量级以内，
	// 而内容帧（工具结果、图片）远大于它。
	controlFrameMax = 8 << 10

	// outboundChanCap 是每条通道的排队深度（帧数）。
	outboundChanCap = 64
)

// outbound 是一条连接的出站队列：预算记账 + 两条优先通道。
//
// 记账语义：queued 只统计**等待写出**的字节。pumpWrites 在开始写之前
// 就把它扣掉，所以单连接的实际占用是 `queued + 一个在写帧`。
// 由此得到放行规则（见 send），占用上界是「预算 + 一帧」。
type outbound struct {
	control chan []byte
	data    chan []byte
	queued  atomic.Int64
	// freed 是归还通知：容量 1、非阻塞发送，只用来唤醒等待者。
	freed chan struct{}
}

func newOutbound() *outbound {
	return &outbound{
		control: make(chan []byte, outboundChanCap),
		data:    make(chan []byte, outboundChanCap),
		freed:   make(chan struct{}, 1),
	}
}

// send 在预算内排入一帧；放弃时调用 cancel（调用方据此断开连接）。
//
// 与旧实现的两点差别：
//   - 超预算时**等待预算归还**（最多 enqueueTimeout），不再立即断连接。
//     以前一次突发就足以踢掉一个只是稍慢的浏览器。
//   - 超过预算的单帧要求**独占队列**（queued==0）才放行：此时占用就是
//     它自己，不会与其它帧叠加成「预算 + 大帧 + 更多」。
func (o *outbound) send(frame []byte, cancel context.CancelFunc) {
	size := int64(len(frame))
	data := size > controlFrameMax
	deadline := time.Now().Add(enqueueTimeout)
	for {
		if size > maxQueueBytes {
			if o.queued.Load() == 0 && o.queued.CompareAndSwap(0, size) {
				o.push(frame, data, cancel)
				return
			}
		} else if o.reserve(size) {
			o.push(frame, data, cancel)
			return
		}
		if !time.Now().Before(deadline) {
			cancel()
			return
		}
		select {
		case <-o.freed:
		case <-time.After(enqueuePoll):
		}
	}
}

// reserve 在预算内预留 n 字节。
func (o *outbound) reserve(n int64) bool {
	for {
		cur := o.queued.Load()
		if cur+n > maxQueueBytes {
			return false
		}
		if o.queued.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// push 把帧放进对应通道；放不进去就归还预算并放弃。
func (o *outbound) push(frame []byte, data bool, cancel context.CancelFunc) {
	size := int64(len(frame))
	ch := o.control
	if data {
		ch = o.data
	}
	select {
	case ch <- frame:
	case <-time.After(enqueueTimeout):
		o.release(size)
		cancel()
	}
}

// release 归还预算并唤醒等待者。
func (o *outbound) release(n int64) {
	o.queued.Add(-n)
	select {
	case o.freed <- struct{}{}:
	default:
	}
}

// pumpWrites 是单连接唯一写协程，保证 WS 写入串行化。
//
// 先排空控制通道：小帧（订阅确认、错误）不该排在大内容帧后面，
// 否则一个正在上传工具结果图片的会话会让控制消息延迟数秒。
func pumpWrites(ctx context.Context, ws *websocket.Conn, o *outbound) {
	for {
		frame, ok := nextFrame(ctx, o)
		if !ok {
			return
		}
		o.release(int64(len(frame)))
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := ws.Write(wctx, websocket.MessageText, frame)
		cancel()
		if err != nil {
			return
		}
	}
}

// nextFrame 取下一帧：控制通道优先，其次数据通道。
func nextFrame(ctx context.Context, o *outbound) ([]byte, bool) {
	select {
	case frame := <-o.control:
		return frame, true
	default:
	}
	select {
	case <-ctx.Done():
		return nil, false
	case frame := <-o.control:
		return frame, true
	case frame := <-o.data:
		return frame, true
	}
}
