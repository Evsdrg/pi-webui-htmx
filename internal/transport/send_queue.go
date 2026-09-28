package transport

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	outboundFrameLimit = 512 << 10
	outboundQueueLimit = 1 << 20
	outboundWait       = 5 * time.Second
)

// enqueueBounded 等待发送者短暂排空队列；等待仅发生在连接的发送协程，
// 不阻塞 Pi 的事件发布。字节预算用 CAS 预留，超时或取消时归还。
func enqueueBounded(ctx context.Context, out chan []byte, queued *atomic.Int64, b []byte) bool {
	if len(b) == 0 || len(b) > outboundFrameLimit {
		return false
	}
	n := int64(len(b))
	for {
		if ctx.Err() != nil {
			return false
		}
		used := queued.Load()
		if used+n <= outboundQueueLimit && queued.CompareAndSwap(used, used+n) {
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Millisecond):
		}
	}
	select {
	case out <- b:
		return true
	case <-ctx.Done():
		queued.Add(-n)
		return false
	}
}
