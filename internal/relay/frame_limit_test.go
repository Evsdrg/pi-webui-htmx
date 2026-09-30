package relay

import (
	"strings"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

// Test转发上限不小于桥允许的浏览器帧 锁住 B53 的核心不变式：
// relay 是转发器，它的单帧上限必须装得下桥允许浏览器发的最大帧
// （图片附件就是这个量级）再加路由封装。历史上这里写死 1 MiB，
// 于是「带图片的消息」在走 relay 的部署里会把整条隧道弄断。
func Test转发上限不小于桥允许的浏览器帧(t *testing.T) {
	want := protocol.BrowserFrameLimit + protocol.RelayEnvelopeBytes
	if maxFrame < want {
		t.Fatalf("relay 单帧上限 %d 小于桥允许的最大帧加封装 %d：合法请求会被当成超限帧断连", maxFrame, want)
	}
}

// Test封装开销不随载荷内容放大 覆盖 B53 的膨胀面：
// 旧实现用 json.Marshal，它默认做 HTML 转义，把载荷里的 < > & 变成 \u003c
// 之类（最多 6 字节），于是「本身没超上限的帧」会在封装后超限被丢。
// relay 只转发、从不渲染，没有理由转义。
func Test封装开销不随载荷内容放大(t *testing.T) {
	payload := []byte(`{"text":"` + strings.Repeat("<&>", 40000) + `"}`)
	for name, wrapped := range map[string][]byte{
		"浏览器方向": routeFromBrowser("tab-1", payload),
		"桥方向":   routeToBrowser("tab-1", payload),
	} {
		if wrapped == nil {
			t.Fatalf("%s：封装失败", name)
		}
		extra := len(wrapped) - len(payload)
		if extra < 0 {
			t.Fatalf("%s：封装后反而变短（%d），说明载荷被改写了", name, extra)
		}
		if extra > protocol.RelayEnvelopeBytes {
			t.Fatalf("%s：封装膨胀 %d 字节，超过预留 %d——载荷被 HTML 转义了", name, extra, protocol.RelayEnvelopeBytes)
		}
	}
}

// Test队列放行超过预算的单帧 覆盖 B53 的第三面：
// 队列预算是 4 MiB，而合法的最大帧远大于它。按预算直接拒绝会让
// 最大的那批帧永远发不出去，所以大帧在**独占队列**时放行。
func Test队列放行超过预算的单帧(t *testing.T) {
	o := newOutbound()
	cancelled := false
	cancel := func() { cancelled = true }

	// 队列为空：大于预算的单帧必须放行。
	big := make([]byte, maxQueueBytes+1)
	o.send(big, cancel)
	if cancelled {
		t.Fatal("队列为空时，大于预算的单帧被拒——最大帧永远发不出去")
	}
	if len(o.data) != 1 {
		t.Fatalf("帧没有入队，队列长度 %d", len(o.data))
	}

	// 队列已占用：大帧要求独占，占用期间不放行也不立刻断连。
	o2 := newOutbound()
	o2.reserve(maxQueueBytes) // 模拟队列里已有等待写出的字节
	done := make(chan struct{})
	go func() { o2.send(make([]byte, maxQueueBytes+1), cancel); close(done) }()
	select {
	case <-done:
		t.Fatal("队列非空时大帧应立即等待预算归还，而不是当场放弃")
	case <-time.After(50 * time.Millisecond):
	}
	if cancelled {
		t.Fatal("等待期间不应取消连接")
	}
	// 预算归还后它应当被放行。
	o2.release(maxQueueBytes)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("预算归还后大帧仍未送出")
	}
	if cancelled {
		t.Fatal("预算已归还，连接不该被取消")
	}
	if len(o2.data) != 1 {
		t.Fatalf("归还预算后大帧应入队，实际 %d", len(o2.data))
	}
}
