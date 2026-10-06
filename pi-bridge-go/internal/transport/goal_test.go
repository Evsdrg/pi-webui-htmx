package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 目标面板只读读取本工作区 .pi/goals，并在桥侧汉化。
// 面板必须：能读到目标文件与账本、认得会话聚焦、用中文标签渲染，且不启动 worker。

func writeGoalFixture(t *testing.T, cwd, id, status, objective string) {
	t.Helper()
	dir := filepath.Join(cwd, ".pi", "goals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"version":3,"id":"` + id + `","status":"` + status + `","autoContinue":true,"sisyphus":false,` +
		`"usage":{"tokensUsed":2500,"activeSeconds":125},` +
		`"taskList":{"blockCompletion":true,"tasks":[` +
		`{"id":"t1","title":"第一步","status":"complete","evidence":"已提交"},` +
		`{"id":"t2","title":"第二步","status":"pending"}]}}`
	content := meta + "\n\n# Goal Prompt\n\n" + objective + "\n\n## Progress\n\n- Status: active\n"
	if err := os.WriteFile(filepath.Join(dir, "active_goal_1_"+id+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGoalLedger(t *testing.T, cwd string, lines ...string) {
	t.Helper()
	dir := filepath.Join(cwd, ".pi", "goals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "goal_events.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeSessionWithFocus 写一个带 pi-goal-focus 自定义条目的会话文件。
func writeSessionWithFocus(t *testing.T, dir, id, cwd, goalID string) {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"custom","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"` + goalID + `","reason":"user"},"id":"f1","parentId":"a","timestamp":"2026-01-01T00:00:02.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func Test目标面板展示聚焦目标与汉化(t *testing.T) {
	requireUI(t)
	s, _, cwd, _, sessionDir := newTestServerTuned(t, 2_000_000_000)
	writeGoalFixture(t, cwd, "g1", "active", "把仓库整理干净")
	writeGoalLedger(t, cwd,
		`{"type":"goal_created","goalId":"g1","at":"2026-01-01T00:00:00Z"}`,
		`{"type":"goal_focused","goalId":"g1","reason":"user","at":"2026-01-01T00:01:00Z"}`,
	)
	writeSessionWithFocus(t, sessionDir, "goal-sess", cwd, "g1")

	rec := getUI(t, s, "/ui/goal?sessionId=goal-sess")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"当前聚焦目标", "进行中", "把仓库整理干净", "已完成", "待办", "最近活动", "已聚焦目标", "1/2 任务"} {
		if !strings.Contains(body, want) {
			t.Fatalf("面板缺少 %q：%s", want, body)
		}
	}
	// 只读面板不启动工作进程。
	if len(s.manager.List()) != 0 {
		t.Fatalf("目标面板不得启动工作进程: %+v", s.manager.List())
	}
}

func Test目标面板无目标时给出说明(t *testing.T) {
	requireUI(t)
	s, _, cwd, _, sessionDir := newTestServerTuned(t, 2_000_000_000)
	// 有会话、但该工作区没有 .pi/goals。
	writeSessionFile(t, sessionDir, "no-goal-sess", cwd)
	rec := getUI(t, s, "/ui/goal?sessionId=no-goal-sess")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "还没有目标") {
		t.Fatalf("无目标时应给出可读说明：%s", rec.Body.String())
	}
}

// 目标徽标（compact）：有聚焦目标时产出可点的徽标，无聚焦目标时为空，
// 让输入栏保持干净。这是「不打开面板也能看出当前处于目标状态」的那条通道。
func Test目标徽标只在聚焦时出现(t *testing.T) {
	requireUI(t)
	s, _, cwd, _, sessionDir := newTestServerTuned(t, 2_000_000_000)
	writeGoalFixture(t, cwd, "g1", "active", "把仓库整理干净")
	writeSessionWithFocus(t, sessionDir, "badge-sess", cwd, "g1")

	rec := getUI(t, s, "/ui/goal?compact=1&sessionId=badge-sess")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "goal-badge") || !strings.Contains(body, "进行中") || !strings.Contains(body, "data-action=\"panel-goal\"") {
		t.Fatalf("聚焦目标时徽标应可见且可点：%s", body)
	}
	// 未聚焦（会话里没有 pi-goal-focus）时徽标为空。
	writeSessionFile(t, sessionDir, "unfocused-sess", cwd)
	if got := getUI(t, s, "/ui/goal?compact=1&sessionId=unfocused-sess").Body.String(); strings.TrimSpace(got) != "" {
		t.Fatalf("未聚焦时徽标应为空：%q", got)
	}
	if len(s.manager.List()) != 0 {
		t.Fatalf("目标徽标不得启动工作进程: %+v", s.manager.List())
	}
}
