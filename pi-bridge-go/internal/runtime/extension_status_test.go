package runtime

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// setStatusRaw 造一条 setStatus 事件载荷。
func setStatusRaw(key, text string) []byte {
	return []byte(`{"type":"extension_ui_request","method":"setStatus","statusKey":` + strconv.Quote(key) + `,"statusText":` + strconv.Quote(text) + `}`)
}

// B36：扩展状态行快照挂在 worker 上。
// 老实现在「转发给浏览器的路径」里更新，于是没有订阅者时漏记。
func Test扩展状态快照不依赖订阅者(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 没有任何订阅者：仍然要记录。
	w.event(setStatusRaw("mc", "mc: 12 (3%) · idle"))
	_, statuses := w.ExtensionStatuses()
	if statuses["mc"] != "mc: 12 (3%) · idle" {
		t.Fatalf("无订阅者时也应记录 setStatus: %+v", statuses)
	}
	// 空文本 = 插件清除该行。
	w.event(setStatusRaw("mc", ""))
	if _, statuses = w.ExtensionStatuses(); len(statuses) != 0 {
		t.Fatalf("空文本应清除该行: %+v", statuses)
	}
	// 其余扩展方法不进快照。
	w.event([]byte(`{"type":"extension_ui_request","method":"notify","message":"hi"}`))
	w.event([]byte(`{"type":"extension_ui_request","method":"setWidget","widgetKey":"w","widgetLines":["a"]}`))
	if _, statuses = w.ExtensionStatuses(); len(statuses) != 0 {
		t.Fatalf("非 setStatus 不应进快照: %+v", statuses)
	}
}

// pi-goal-x 的状态行在桥侧汉化后再进快照：面板/状态栏读到的都应是中文。
func Test扩展状态行汉化goal(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.event(setStatusRaw("goal", "goal: unfocused [3 open] - /goal-focus"))
	_, statuses := w.ExtensionStatuses()
	if statuses["goal"] != "目标：未聚焦（3 个进行中）· /goal-focus" {
		t.Fatalf("goal 状态行应被汉化: %q", statuses["goal"])
	}
	// 其它插件的状态行不受影响。
	w.event(setStatusRaw("mc", "mc: 12 (3%) · idle"))
	if _, statuses = w.ExtensionStatuses(); statuses["mc"] != "mc: 12 (3%) · idle" {
		t.Fatalf("非 goal 状态行不应被改写: %+v", statuses)
	}
}

// B36 的核心：不同 worker 的同名 key 不再互相覆盖。
// 老实现是传输层的全局 byKey，后收到的 setStatus 会把先前的顶掉，
// 切会话时就会看到另一个会话的扩展状态。
func Test扩展状态按worker隔离(t *testing.T) {
	w1 := &Worker{extStatuses: map[string]string{}}
	w2 := &Worker{extStatuses: map[string]string{}}
	w1.applyStatusLocked("mc", "A")
	w2.applyStatusLocked("mc", "B")
	_, s1 := w1.ExtensionStatuses()
	_, s2 := w2.ExtensionStatuses()
	if s1["mc"] != "A" || s2["mc"] != "B" {
		t.Fatalf("同名 key 应各自独立: w1=%+v w2=%+v", s1, s2)
	}
}

// 快照有界；换 epoch（rebind/新会话）后清空——旧进程的状态不属于新会话。
func Test扩展状态快照有界且换epoch清空(t *testing.T) {
	m, cwd := newTestManager(t)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxStatusKeys+10; i++ {
		w.event(setStatusRaw("k"+strconv.Itoa(i), "v"))
	}
	if _, statuses := w.ExtensionStatuses(); len(statuses) != maxStatusKeys {
		t.Fatalf("快照应有界为 %d，实际 %d", maxStatusKeys, len(statuses))
	}
	// 超长的 key/text 直接拒绝。
	w.event(setStatusRaw(strings.Repeat("k", 200), "v"))
	w.event(setStatusRaw("long-text", strings.Repeat("x", 5000)))
	if _, statuses := w.ExtensionStatuses(); len(statuses) != maxStatusKeys {
		t.Fatalf("超长 key/text 不应进快照，实际 %d", len(statuses))
	}
	before := w.Info().Epoch
	w.resetReplay()
	epoch, statuses := w.ExtensionStatuses()
	if len(statuses) != 0 {
		t.Fatalf("换 epoch 后应清空: %+v", statuses)
	}
	if epoch == "" || epoch == before {
		t.Fatalf("epoch 应已更换: %q -> %q", before, epoch)
	}
}
