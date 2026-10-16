package runtime

import (
	"context"
	"testing"
)

// 回合收尾不能只认 agent_settled。
//
// 为什么需要兜底：桥的忙标志（active）由 agent_start 之类的事件置真、只由
// agent_settled 置假。而 agent_settled 只是一条小事件，一旦它在途中丢失
// （订阅被摘除、事件被省略后没有触发重同步等），active 就永远为真——
// busyLocked() 恒真，session.navigate（「从此处编辑」的桥侧实现）会一直
// 以 busy 拒绝，用户从此再也点不动编辑，只有中止或重启才能恢复。
//
// agent_end 是同一轮里更早、更可靠发出的收尾信号：Pi 在 agent_end 上带
// willRetry，false 表示这一轮不再重试。用它兜底置假，结算事件丢失就不再
// 演变成永久卡死。willRetry 为真时保持忙（重试即将到来，auto_retry_start
// 会重新置真，这里不能提前置假）。
func Test回合结束事件兜底复位忙标志(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}

	w.event([]byte(`{"type":"agent_start"}`))
	if !w.Info().Busy {
		t.Fatal("agent_start 后应处于忙状态")
	}

	// 结算事件丢失：只发到 agent_end（不重试）。
	w.event([]byte(`{"type":"agent_end","willRetry":false}`))
	if w.Info().Busy {
		t.Fatal("agent_end(willRetry=false) 后不应再忙：结算事件丢失会让桥永久拒绝 navigate")
	}
	if got := w.Info().Status; got != "idle" {
		t.Fatalf("agent_end(willRetry=false) 后状态应为 idle，实际 %q", got)
	}
}

// willRetry 为真时不得提前置假：重试是同一轮的延续。
func Test重试前的回合结束不复位忙标志(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}

	w.event([]byte(`{"type":"agent_start"}`))
	w.event([]byte(`{"type":"agent_end","willRetry":true}`))
	if !w.Info().Busy {
		t.Fatal("agent_end(willRetry=true) 是重试前兆，不应复位忙标志")
	}
}
