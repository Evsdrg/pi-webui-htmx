package presentation

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// 工具块的成功/失败配色由 toolResult.isError 决定。这条链路断了
// （比如把 isError 读错字段）不会报错，只会让所有工具块都显示成成功，
// 用户因此看不出哪一步失败了——所以必须锁。
func Test工具块按成败着色(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:12.177Z","message":{"role":"toolResult","toolName":"bash","isError":false,"content":[{"type":"text","text":"ok"}]}}`),
			json.RawMessage(`{"type":"message","id":"t2","timestamp":"2026-09-21T13:33:18.311Z","message":{"role":"toolResult","toolName":"grep","isError":true,"content":[{"type":"text","text":"no match"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 工具名必须来自记录里的 toolName，不能一律显示「工具」。
	for _, name := range []string{">bash<", ">grep<"} {
		if !strings.Contains(html, name) {
			t.Fatalf("应显示真实工具名 %s：%s", name, html)
		}
	}
	okBlock := blockByEntry(t, html, "t1")
	errBlock := blockByEntry(t, html, "t2")
	if !strings.Contains(okBlock, `data-ok="true"`) {
		t.Fatalf("成功步骤应标记 data-ok=true：%s", okBlock)
	}
	if !strings.Contains(errBlock, `data-ok="false"`) {
		t.Fatalf("失败步骤应标记 data-ok=false：%s", errBlock)
	}
}

// 时长由条目时间戳推导，不足 1 秒不显示。这条与 Pi Web 的
// `secs > 0 ? secs : undefined` 一致：给 30 毫秒的步骤显示 "0s" 只会干扰阅读。
func Test工具时长按整秒推导且亚秒不显示(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			// 亚秒：assistant 12.135 → 12.177，只有 42 毫秒。
			json.RawMessage(`{"type":"message","id":"t1","timestamp":"2026-09-21T13:33:12.177Z","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok"}]}}`),
			// 跨秒：同一 assistant 到 18.311，约 6 秒。
			json.RawMessage(`{"type":"message","id":"t2","timestamp":"2026-09-21T13:33:18.311Z","message":{"role":"toolResult","toolName":"grep","content":[{"type":"text","text":"x"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	fast := blockByEntry(t, html, "t1")
	if strings.Contains(fast, "tool-duration") {
		t.Fatalf("亚秒步骤不应显示时长：%s", fast)
	}
	slow := blockByEntry(t, html, "t2")
	if !strings.Contains(slow, "6s") {
		t.Fatalf("6 秒步骤应显示 6s：%s", slow)
	}
}

// 思考时长 = 本回合首个 assistant 条目时间戳 − 前一条目时间戳。
// 这正是 Pi Web 的 thinkingDurationFromFile 推导。
func Test思考时长取生成间隔(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","timestamp":"2026-09-21T13:33:04.142Z","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","timestamp":"2026-09-21T13:33:12.135Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":""},{"type":"text","text":"好"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 04.142 → 12.135 约 8 秒。
	if !strings.Contains(html, "8s") {
		t.Fatalf("应显示 8s 思考时长：%s", html)
	}
}

// 时间戳缺失时不能把零值当成 1970 年算出离谱时长。
func Test时间戳缺失时不显示时长(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑一下"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"好"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "tool-duration") || strings.Contains(html, "thinking-duration") {
		t.Fatalf("无时间戳时不应显示时长：%s", html)
	}
}

// blockByEntry 按 tool-call 的 data-entry-id 取单块。
// 直接搜整篇会把别的步骤的时长算进来。
func blockByEntry(t *testing.T, html, id string) string {
	t.Helper()
	marker := `data-entry-id="` + id + `"`
	start := strings.Index(html, marker)
	if start < 0 {
		t.Fatalf("找不到条目 %s", id)
	}
	// 回到所属的 <details 开头，取到下一个 <details 或 </div> 之前。
	open := strings.LastIndex(html[:start], "<details")
	if open < 0 {
		t.Fatalf("条目 %s 不在 details 内", id)
	}
	rest := html[open:]
	// 从开标签之后开始找下一个 <details：rest 自身就以 <details 开头，
	// 从 0 找会命中自己，于是把后面所有步骤都算进来。
	if end := strings.Index(rest[len("<details"):], "<details"); end >= 0 {
		return rest[:len("<details")+end]
	}
	if end := strings.Index(rest, "</div>"); end > 0 {
		return rest[:end]
	}
	return rest
}
