package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"pi-bridge-go/internal/protocol"
)

// 有 RPC 在途不等于「回合在跑」。
//
// busyLocked 把 pending（在途命令数）算进了忙碌判定，于是任何并发查询都会
// 让导航/重载短暂地看到 busy。生产上的表现是：模型请求失败（界面已显示
// 「供应商服务端故障」）后点「从此处编辑」，会随机收到「会话正在运行，请
// 先等待当前回合结束」——而那一刻会话其实是空闲的，只是前端每 15 秒一次
// 的 session.state 轮询恰好有命令在途。
//
// 实测（lab，空闲会话 + 并发轮询）：30 次 session.navigate 有 4 次被误拒。
// 轮询越快比例越高，用户点得越巧越容易撞上。
//
// 判定必须分成两个语义：
//   - turnRunningLocked：回合是否在跑（active/queued/uncertain/waitingInput）。
//     它决定「能不能编辑、能不能跳转、能不能重载」。
//   - busyLocked：是否还有结果未定的工作（上面四项 + 在途命令）。
//     它只该用于「别急着回收/重启」这类需要等待静默的场合。
func Test在途命令不算回合运行(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if w.turnRunningLocked() {
		t.Fatal("刚启动的 worker 不应视为回合在跑")
	}

	// 模拟一条在途命令：pending 加一，其余状态都不变。
	w.mu.Lock()
	w.pending++
	pendingBusy := w.busyLocked()
	turnRunning := w.turnRunningLocked()
	w.pending--
	w.mu.Unlock()

	if !pendingBusy {
		t.Fatal("有在途命令时 busyLocked 应为真（用于空闲回收与强制停止的判定）")
	}
	if turnRunning {
		t.Fatal("有在途命令但回合没跑，turnRunningLocked 必须为假——否则「从此处编辑」会被误拒")
	}
}

// 回合真的在跑时，编辑/跳转仍必须被拒。
func Test回合运行时可拒绝编辑(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 走真实事件入口：agent_start 把 active 置真。
	w.event([]byte(`{"type":"agent_start"}`))
	if !w.turnRunningLocked() {
		t.Fatal("agent_start 之后应视为回合在跑")
	}
	w.event([]byte(`{"type":"agent_settled"}`))
	if w.turnRunningLocked() {
		t.Fatal("agent_settled 之后不应再视为回合在跑")
	}
}

// 等待人工输入时同样不能编辑：此时 Pi 在等回执，跳转会打断它。
func Test等待输入时不可编辑(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.injectDialogForTest(t, "dlg-edit", `{"type":"extension_ui_request","id":"dlg-edit","method":"confirm","title":"要吗"}`)
	if !w.turnRunningLocked() {
		t.Fatal("等待人工输入时应视为回合在跑，编辑必须被拒")
	}
}

// 结果未定的变更命令（uncertain）也必须挡住编辑：
// 此时 Pi 可能正在写，跳转会让叶子归属串线。
func Test结果未定时不可编辑(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.uncertain = true
	running := w.turnRunningLocked()
	w.mu.Unlock()
	if !running {
		t.Fatal("结果未定时应视为回合在跑")
	}
}

// 空闲回收仍要看 pending：有命令在途时不能回收进程。
func Test在途命令仍挡住空闲回收(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.IdleTimeout = 10 * time.Millisecond })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.pending++
	busy := w.busyLocked()
	w.mu.Unlock()
	if !busy {
		t.Fatal("有在途命令时不得被空闲回收")
	}
}

// 端到端反例：在途命令存在时，Navigate 必须越过忙检查进入 Pi。
//
// 这条直接盯住用户看到的那句提示。旧实现在这里返回 busy，新实现会继续
// 往下走，最终因为「跳转扩展未启用」或「条目不存在」而失败——但**不是**
// busy。只要错误码不再是 busy，就说明判据已经换成回合状态。
func Test在途命令不挡住跳转(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.pending++
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.pending--
		w.mu.Unlock()
	}()

	_, err = w.Navigate(ctx, "u1")
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Code == "busy" {
		t.Fatalf("有在途命令但回合没跑，跳转不应报 busy：%v", err)
	}
}

// 反过来：回合真的在跑时，跳转仍必须被拒。
func Test回合运行时跳转被拒(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.event([]byte(`{"type":"agent_start"}`))
	_, err = w.Navigate(ctx, "u1")
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != "busy" {
		t.Fatalf("回合运行中跳转必须报 busy，实际 %v", err)
	}
}
