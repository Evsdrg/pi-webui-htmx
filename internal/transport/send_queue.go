package transport

import (
	"context"
	"sync"
	"time"
)

const (
	outboundFrameLimit = 512 << 10
	outboundQueueLimit = 1 << 20
	outboundWait       = 5 * time.Second
)

// outboundQueue 是单条连接（WebSocket 或隧道上的虚拟连接）的出站帧队列
// 与字节预算。
//
// 唯一消费者是连接自己的写协程；生产者可能有多个（命令回执、事件推送、
// 补发循环），因此等待与唤醒必须对多生产者成立。
//
// 为什么不是「CAS 抢占 + 轮询」：早先的实现是
//
//	for { if CAS(used, used+n) { break }; select { case <-time.After(2ms): } }
//
// 两个问题。一是等待延迟：配额被归还的那一刻不发通知，等待方最坏要等
// 一个轮询周期。二是唤醒成本：等待方每 2ms 醒一次去读一个没变的数，
// 与等待时长成正比（每个等待者每秒醒 500 次）。两者叠加会放大大批补发
// 的耗时——正是补发队列那次修复处理的场景。
//
// 现在的约定：**检查条件与挂载等待在同一把锁下**，归还时广播。
//
//   - 归还（唯一的调用点是消费者取走一帧）在锁内判断「有没有人在等」，
//     有人等才 close 掉当前代次的信号通道并换新通道——只在真有竞争时
//     才分配，平稳路径上零分配。
//   - 等待方持锁读到「已满」并取走信号通道后才睡眠，因此不存在漏唤醒：
//     归还若发生在取通道之后，它会在锁内看到 waiting > 0 并 close 掉
//     那正好是等待方手里的通道。
//   - close 是广播而不是单播：一次归还可能够好几个等待者用，只叫醒一个
//     会让其余的等下一次归还，白白多绕一圈消费者。
type outboundQueue struct {
	frames chan []byte

	mu      sync.Mutex
	used    int64
	waiting int
	space   chan struct{}
}

func newOutboundQueue(depth int) *outboundQueue {
	return &outboundQueue{
		frames: make(chan []byte, depth),
		space:  make(chan struct{}),
	}
}

// enqueue 在字节预算内排入一帧，预算不足时等待消费者归还。
// 等待由 ctx 约束：调用方用 context.WithTimeout(…, outboundWait) 表达
// 「这次投递的时间预算」。返回 false 表示放弃（调用方据此决定是否断开）。
func (q *outboundQueue) enqueue(ctx context.Context, b []byte) bool {
	if len(b) == 0 || len(b) > outboundFrameLimit {
		return false
	}
	n := int64(len(b))
	if !q.reserve(ctx, n) {
		return false
	}
	select {
	case q.frames <- b:
		return true
	case <-ctx.Done():
		q.release(n)
		return false
	}
}

// reserve 预留 n 字节的出站预算，不足时等待归还。
func (q *outboundQueue) reserve(ctx context.Context, n int64) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		q.mu.Lock()
		if q.used+n <= outboundQueueLimit {
			q.used += n
			q.mu.Unlock()
			return true
		}
		space := q.space
		q.waiting++
		q.mu.Unlock()

		select {
		case <-space:
		case <-ctx.Done():
			q.mu.Lock()
			q.waiting--
			q.mu.Unlock()
			return false
		}
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
	}
}

// release 归还 n 字节并唤醒等待者。
// 消费者取走一帧后调用它（含入队被取消时的回滚），这是唯一的归还点。
func (q *outboundQueue) release(n int64) {
	q.mu.Lock()
	q.used -= n
	if q.waiting > 0 {
		// 有人等：换一代信号通道。close 广播给全部等待者，
		// 让它们各自重新检查——一次归还可能够好几个人用。
		close(q.space)
		q.space = make(chan struct{})
	}
	q.mu.Unlock()
}

// bytes 返回当前占用的预算，供诊断与测试使用。
func (q *outboundQueue) bytes() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.used
}
