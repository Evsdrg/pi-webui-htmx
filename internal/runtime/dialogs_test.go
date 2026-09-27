package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func Test扩展对话登记与回复(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.Env = []string{"FAKE_PI_SCRIPT=extension_dialog"} })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() { _ = w.Prompt(ctx, "触发对话", "", nil) }()

	// 等对话请求出现，并确认 worker 进入等待输入状态。
	deadline := time.Now().Add(3 * time.Second)
	sawDialog := false
	for time.Now().Before(deadline) && !sawDialog {
		msg, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if msg.Event != "pi.event" {
			continue
		}
		raw, _ := json.Marshal(msg.Data)
		if _, ok := ParseDialog(raw); ok {
			sawDialog = true
		}
	}
	if !sawDialog {
		t.Fatal("未收到扩展对话请求")
	}
	ids := w.PendingDialogs()
	if len(ids) != 1 {
		t.Fatalf("应登记一个待回复对话: %v", ids)
	}
	// 等待输入期间不得被当成空闲回收。
	if !w.Info().Busy {
		t.Fatal("等待人工输入时应判定为忙")
	}
	if w.Info().Status != "waiting_input" {
		t.Fatalf("状态应为 waiting_input，实际 %s", w.Info().Status)
	}

	value := "允许"
	if err := w.UIResponse(ctx, ids[0], &value, nil, false); err != nil {
		t.Fatalf("回复对话失败: %v", err)
	}
	if len(w.PendingDialogs()) != 0 {
		t.Fatal("回复后待回复列表应为空")
	}
	if err := w.UIResponse(ctx, "不存在的ID", nil, nil, true); err == nil {
		t.Fatal("回复未知对话应报错")
	}
}

func Test回复参数校验(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.UIResponse(ctx, "", nil, nil, false); err == nil {
		t.Fatal("空 ID 必须被拒绝")
	}
	if err := w.UIResponse(ctx, "x", nil, nil, false); err == nil {
		t.Fatal("必须提供 value、confirmed 或 cancelled 之一")
	}
}

func TestParseDialog拒绝非对话事件(t *testing.T) {
	if _, ok := ParseDialog(json.RawMessage(`{"type":"agent_start"}`)); ok {
		t.Fatal("普通事件不应被解析为对话")
	}
	if _, ok := ParseDialog(json.RawMessage(`{"type":"extension_ui_request"}`)); ok {
		t.Fatal("缺少 ID 的请求不应被解析为对话")
	}
	d, ok := ParseDialog(json.RawMessage(`{"type":"extension_ui_request","id":"u1","method":"select","title":"选择","options":["a","b"],"timeout":5000}`))
	if !ok || d.Method != "select" || len(d.Options) != 2 || d.TimeoutMs != 5000 {
		t.Fatalf("对话解析异常: %+v %v", d, ok)
	}
}

func Test停止时取消未回复对话(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.Env = []string{"FAKE_PI_SCRIPT=extension_dialog"} })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() { _ = w.Prompt(ctx, "触发对话", "", nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(w.PendingDialogs()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(w.PendingDialogs()) == 0 {
		t.Fatal("未登记到对话")
	}
	w.CancelPendingDialogs()
	if len(w.PendingDialogs()) != 0 {
		t.Fatal("取消后不应残留待回复对话")
	}
	if w.Info().Busy {
		t.Fatal("取消后不应仍判定为忙")
	}
}

func Test停止时取消待回复对话且不永久挂起(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.Env = []string{"FAKE_PI_SCRIPT=extension_dialog"} })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() { _ = w.Prompt(ctx, "触发对话", "", nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(w.PendingDialogs()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(w.PendingDialogs()) == 0 {
		t.Fatal("未登记到对话")
	}
	// 带 pending dialog 时不能普通停止，必须显式 force。
	if err := w.Stop(false); err == nil {
		t.Fatal("有待回复对话时应拒绝非强制停止")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- w.Stop(true) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("强制停止失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("强制停止未在时限内完成，Pi 可能仍在等待对话")
	}
	if len(w.PendingDialogs()) != 0 {
		t.Fatal("停止后不应残留待回复对话")
	}
}

func Test事件计数接入指标(t *testing.T) {
	sink := &recordingSink{}
	m, cwd := newTestManager(t, func(c *Config) { c.Metrics = sink })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := w.Prompt(ctx, "hi", "", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w.Info().Seq > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w.Info().Seq == 0 {
		t.Fatal("未产生任何事件")
	}
	if sink.published.Load() == 0 {
		t.Fatal("事件计数未接入指标")
	}
	if sink.started.Load() == 0 {
		t.Fatal("工作进程启动计数未接入指标")
	}
}

// recordingSink 记录指标回调次数，用于验证接入点没被漏掉。
type recordingSink struct {
	published atomic.Int64
	started   atomic.Int64
	reaped    atomic.Int64
	exited    atomic.Int64
	dropped   atomic.Int64
	expired   atomic.Int64
}

func (r *recordingSink) EventPublished()      { r.published.Add(1) }
func (r *recordingSink) WorkerStarted()       { r.started.Add(1) }
func (r *recordingSink) WorkerReaped()        { r.reaped.Add(1) }
func (r *recordingSink) WorkerExited()        { r.exited.Add(1) }
func (r *recordingSink) EventDropped()        { r.dropped.Add(1) }
func (r *recordingSink) DialogsExpired(n int) { r.expired.Add(int64(n)) }

func Test无需回执的扩展方法不登记为对话(t *testing.T) {
	// setStatus/setWidget/notify/setTitle/set_editor_text 在 Pi RPC 模式是
	// fire-and-forget，一旦被当成待回复对话，worker 会永久停在 waiting_input
	// 并挡住空闲回收。
	for _, method := range []string{"setStatus", "setWidget", "notify", "setTitle", "set_editor_text"} {
		t.Run(method, func(t *testing.T) {
			m, cwd := newTestManager(t)
			ctx := context.Background()
			w, err := m.Start(ctx, "", cwd)
			if err != nil {
				t.Fatal(err)
			}
			sub, _, err := w.Subscribe()
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			// 直接走事件入口，模拟 Pi 推来的帧。
			w.EventForTest(json.RawMessage(`{"type":"extension_ui_request","id":"x-` + method +
				`","method":"` + method + `","statusKey":"k","statusText":"t"}`))
			if len(w.PendingDialogs()) != 0 {
				t.Fatalf("%s 不应登记为待回复对话", method)
			}
			if w.Info().Status == "waiting_input" {
				t.Fatalf("%s 不应让 worker 进入 waiting_input", method)
			}
			if w.Info().Busy {
				t.Fatalf("%s 不应让 worker 判定为忙", method)
			}
		})
	}
}

func Test需要回执的扩展方法仍登记为对话(t *testing.T) {
	for _, method := range []string{"select", "confirm", "input", "editor"} {
		t.Run(method, func(t *testing.T) {
			m, cwd := newTestManager(t)
			ctx := context.Background()
			w, err := m.Start(ctx, "", cwd)
			if err != nil {
				t.Fatal(err)
			}
			sub, _, err := w.Subscribe()
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			w.EventForTest(json.RawMessage(`{"type":"extension_ui_request","id":"d-` + method +
				`","method":"` + method + `","title":"确认？"}`))
			ids := w.PendingDialogs()
			if len(ids) != 1 || ids[0] != "d-"+method {
				t.Fatalf("%s 应登记为待回复对话: %v", method, ids)
			}
			if !w.Info().Busy {
				t.Fatalf("%s 应让 worker 判定为忙", method)
			}
			if w.Info().Status != "waiting_input" {
				t.Fatalf("%s 应进入 waiting_input，实际 %s", method, w.Info().Status)
			}
			// 回复后立即可用。
			if err := w.UIResponse(ctx, ids[0], nil, nil, true); err != nil {
				t.Fatal(err)
			}
			if len(w.PendingDialogs()) != 0 {
				t.Fatal("回复后不应残留")
			}
		})
	}
}

// Test非法回执后对话仍可重试 覆盖 B16：
// 参数校验以前排在摘除对话之前，一次非法回执就让合法重试变成 not_found，
// 而 Pi 仍在等待。
func Test非法回执后对话仍可重试(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 通过真实事件流登记一个对话。
	w.injectDialogForTest(t, "dlg-1", `{"type":"extension_ui_request","id":"dlg-1","method":"confirm","title":"确认？"}`)
	// 非法回执：三种语义都没提供。
	if err := w.UIResponse(ctx, "dlg-1", nil, nil, false); err == nil {
		t.Fatal("非法回执应被拒绝")
	}
	// 对话必须还在，否则用户无法修正重试。
	if _, ok := w.PendingDialog("dlg-1"); !ok {
		t.Fatal("非法回执把对话从表里删掉了")
	}
	// 合法重试必须成功。
	yes := true
	if err := w.UIResponse(ctx, "dlg-1", nil, &yes, false); err != nil {
		t.Fatalf("合法重试应成功: %v", err)
	}
	if _, ok := w.PendingDialog("dlg-1"); ok {
		t.Fatal("成功回复后对话应被移除")
	}
}

// Test对话超时后worker可回收 覆盖 B48：
// Pi 到期会自行解决 pending 请求且不通知桥，桥必须自己清理，
// 否则 waitingInput 永远为真，worker 永远得不到空闲回收资格。
func Test对话超时后worker可回收(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.IdleTimeout = 20 * time.Millisecond })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 带 timeout 的对话：Pi 会在 30ms 后自行解决。
	w.injectDialogForTest(t, "dlg-to", `{"type":"extension_ui_request","id":"dlg-to","method":"confirm","title":"等一会","timeout":30}`)
	if !w.busyLocked() {
		t.Fatal("有待回复对话时 worker 应视为忙")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w.ExpireDialogs(time.Now()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := w.PendingDialog("dlg-to"); ok {
		t.Fatal("超时对话未被清理")
	}
	if w.busyLocked() {
		t.Fatal("超时对话清理后 worker 仍视为忙，空闲回收被永久挡住")
	}
	// 没有 timeout 字段的对话不能被误清理。
	w.injectDialogForTest(t, "dlg-keep", `{"type":"extension_ui_request","id":"dlg-keep","method":"confirm","title":"一直等"}`)
	time.Sleep(80 * time.Millisecond)
	if n := w.ExpireDialogs(time.Now()); n != 0 {
		t.Fatalf("无 timeout 的对话不应被清理: %d", n)
	}
	if _, ok := w.PendingDialog("dlg-keep"); !ok {
		t.Fatal("无 timeout 的对话被误清")
	}
}

// Test超大对话事件仍占住对话 覆盖 B67：
// 超过 EventBytes 的 extension_ui_request 以前在登记前就被省略，
// Pi 在等回执而桥没有记录，扩展永久挂起。
func Test超大对话事件仍占住对话(t *testing.T) {
	m, cwd := newTestManager(t, func(c *Config) { c.EventBytes = 1024 })
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", 4096)
	payload := `{"type":"extension_ui_request","id":"dlg-big","method":"input","title":"大对话","prefill":"` + huge + `"}`
	w.injectDialogForTest(t, "dlg-big", payload)
	if _, ok := w.PendingDialog("dlg-big"); !ok {
		t.Fatal("超大对话事件未登记，Pi 会永久等待回执")
	}
	if !w.busyLocked() {
		t.Fatal("超大对话未让 worker 进入等待状态")
	}
	// 前端仍应能回复它。
	value := "ok"
	if err := w.UIResponse(ctx, "dlg-big", &value, nil, false); err != nil {
		t.Fatalf("超大对话应可回复: %v", err)
	}
}

// injectDialogForTest 直接走 worker 的事件入口登记一个对话，
// 与真实 Pi 推送走同一条代码路径。
func (w *Worker) injectDialogForTest(t *testing.T, id, payload string) {
	t.Helper()
	if _, ok := ParseDialog(json.RawMessage(payload)); !ok {
		t.Fatalf("夹具不是合法对话: %s", payload)
	}
	w.event(json.RawMessage(payload))
	if _, ok := w.PendingDialog(id); !ok {
		t.Fatalf("对话 %s 未被登记", id)
	}
}
