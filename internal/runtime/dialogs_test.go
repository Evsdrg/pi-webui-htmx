package runtime

import (
	"context"
	"encoding/json"
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
	go func() { _ = w.Prompt(ctx, "触发对话", "") }()

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
	go func() { _ = w.Prompt(ctx, "触发对话", "") }()
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
