package goal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoalFile 造一个目标文件：文件头是 JSON 元数据，其后是 Markdown 正文。
func writeGoalFile(t *testing.T, dir, name string, meta map[string]any, objective string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	content := string(body) + "\n\n# Goal Prompt\n\n" + objective + "\n\n## Progress\n\n- Status: active\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func Test解析目标文件(t *testing.T) {
	dir := t.TempDir()
	writeGoalFile(t, dir, "active_goal_1_g1.md", map[string]any{
		"version":      3,
		"id":           "g1",
		"objective":    "旧的目标（会被正文覆盖）",
		"status":       "active",
		"autoContinue": true,
		"sisyphus":     false,
		"usage":        map[string]any{"tokensUsed": 1234, "activeSeconds": 90},
		"taskList": map[string]any{
			"blockCompletion": true,
			"tasks": []any{
				map[string]any{"id": "t1", "title": "第一步", "status": "complete"},
				map[string]any{"id": "t2", "title": "第二步", "status": "pending"},
			},
		},
	}, "把仓库整理干净")
	g, ok := parseGoalFile(filepath.Join(dir, "active_goal_1_g1.md"))
	if !ok {
		t.Fatal("应能解析")
	}
	if g.ID != "g1" || g.Status != "active" || !g.AutoContinue {
		t.Fatalf("元数据解析异常: %+v", g)
	}
	if g.Objective != "把仓库整理干净" {
		t.Fatalf("objective 应取自正文而非元数据: %q", g.Objective)
	}
	if g.Usage.TokensUsed != 1234 || g.Usage.ActiveSeconds != 90 {
		t.Fatalf("usage 解析异常: %+v", g.Usage)
	}
	if total, done := g.TaskCounts(); total != 2 || done != 1 {
		t.Fatalf("任务计数应为 2/1: %d/%d", total, done)
	}
	if !g.BlockCompletion() {
		t.Fatal("应解析出 blockCompletion")
	}
}

// 正文里的 '}' 不能干扰 JSON 头部的结束位置判定。
func Test正文里的花括号不干扰解析(t *testing.T) {
	dir := t.TempDir()
	writeGoalFile(t, dir, "active_goal_2_g2.md", map[string]any{"version": 3, "id": "g2", "status": "paused"}, "修好 {a:1} 这种样例，并处理 } 结尾")
	g, ok := parseGoalFile(filepath.Join(dir, "active_goal_2_g2.md"))
	if !ok || g.ID != "g2" {
		t.Fatalf("含花括号的正文应仍能解析: %v %+v", ok, g)
	}
	if !strings.Contains(g.Objective, "{a:1}") {
		t.Fatalf("objective 应保留花括号原文: %q", g.Objective)
	}
}

// 损坏的文件（没有 JSON 头）应被判为不可解析，而不是 panic 或返回半份数据。
func Test损坏文件被跳过(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "active_goal_x_bad.md"), []byte("这不是 JSON 头"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := parseGoalFile(filepath.Join(dir, "active_goal_x_bad.md")); ok {
		t.Fatal("无 JSON 头的文件应解析失败")
	}
}

func TestLoad包含进行中与聚焦目标(t *testing.T) {
	dir := t.TempDir()
	writeGoalFile(t, dir, "active_goal_1_g1.md", map[string]any{"version": 3, "id": "g1", "status": "active", "updatedAt": "2026-01-02T00:00:00Z"}, "目标一")
	writeGoalFile(t, dir, "active_goal_2_g2.md", map[string]any{"version": 3, "id": "g2", "status": "paused", "updatedAt": "2026-01-03T00:00:00Z"}, "目标二")
	// 一个已归档但仍在聚焦的目标。
	writeGoalFile(t, filepath.Join(dir, "archived"), "goal_3_g3.md", map[string]any{"version": 3, "id": "g3", "status": "complete"}, "已完成的目标")

	view := Load(dir, "g3", 10)
	if !view.Available {
		t.Fatal("目录存在时 Available 应为真")
	}
	if len(view.Goals) != 2 {
		t.Fatalf("应有 2 个进行中目标: %+v", view.Goals)
	}
	if view.Goals[0].ID != "g2" {
		t.Fatalf("进行中目标应按更新时间倒序（g2 更新更晚）: %+v", view.Goals)
	}
	if view.Focused == nil || view.Focused.ID != "g3" || !view.Focused.Archived {
		t.Fatalf("聚焦目标应能解析到已归档项: %+v", view.Focused)
	}
}

func TestLoad目录缺失时给出说明(t *testing.T) {
	view := Load(filepath.Join(t.TempDir(), "不存在"), "", 10)
	if view.Available {
		t.Fatal("目录不存在时 Available 应为假")
	}
	if view.Notice == "" {
		t.Fatal("应给出可读说明，而不是空白")
	}
}

func Test账本过滤并按新在前(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"goal_created","goalId":"g1","at":"2026-01-01T00:00:00Z"}`,
		`{"type":"goal_created","goalId":"g2","at":"2026-01-01T00:01:00Z"}`,
		`{"type":"task_complete","goalId":"g1","taskId":"t1","at":"2026-01-01T00:02:00Z"}`,
		`{"type":"audit_result","goalId":"g1","verdict":"approved","report":"ok","at":"2026-01-01T00:03:00Z"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "goal_events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := readLedger(filepath.Join(dir, "goal_events.jsonl"), "g1", 10)
	if len(events) != 3 {
		t.Fatalf("应只取 g1 的事件: %+v", events)
	}
	if events[0].Type != "audit_result" {
		t.Fatalf("应为新的在前: %+v", events)
	}
	// 上限生效。
	limited := readLedger(filepath.Join(dir, "goal_events.jsonl"), "g1", 1)
	if len(limited) != 1 || limited[0].Type != "audit_result" {
		t.Fatalf("上限应只留最新一条: %+v", limited)
	}
}

func Test读取会话聚焦(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	lines := []string{
		`{"type":"message","message":{"role":"user"}}`,
		`{"type":"custom","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"g1","reason":"user"}}`,
		`{"type":"custom","customType":"pi-goal-draft","data":{"version":1}}`,
		`{"type":"custom","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"g2","reason":"user"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _, found := ReadFocus(path)
	if !found || id != "g2" {
		t.Fatalf("应取最后一次聚焦: found=%v id=%q", found, id)
	}
	// 不存在的文件不报错、返回未找到。
	if _, _, ok := ReadFocus(filepath.Join(dir, "没有这个文件")); ok {
		t.Fatal("文件不存在应返回未找到")
	}
}

func Test汉化覆盖插件真实文案(t *testing.T) {
	// 这些是从 pi-goal-x 源码里逐条摘出的真实文案（notify / 对话框标题），
	// 用来锁住翻译表：插件文案若变化，这里会先失败，提示补翻译。
	cases := []struct{ in, want string }{
		// notify
		{"Goal paused.", "目标已暂停。"},
		{"Goal resumed; autonomous allowance renewed.", "目标已恢复；自动运行额度已重置。"},
		{"No goal is set.", "未设置目标。"},
		{"Goal focus unchanged.", "目标聚焦未变更。"},
		{"Goal is already paused. Use /goal-resume to continue.", "目标已处于暂停状态。使用 /goal-resume 继续。"},
		{"Auditor enabled for this goal.", "已为该目标启用审计器。"},
		{"No focused goal to toggle the auditor for.", "没有聚焦的目标，无法切换审计器。"},
		{"Goal archived.\nFile: /opt/x/.pi/goals/archived/goal.md", "目标已归档。\n文件：/opt/x/.pi/goals/archived/goal.md"},
		{"Token budget 80% used (8000/10000 tokens). Use /goal-tweak to change or remove the budget.", "Token 预算已用 80%（8000/10000 tokens）。可用 /goal-tweak 修改或移除预算。"},
		{"Goal stalled: no activity for 15 minutes.", "目标停滞：已 15 分钟无活动。"},
		{"Goal owned by another session. Use /goal-resume to take ownership.", "目标归属另一会话。使用 /goal-resume 取得所有权。"},
		{"Goal scheduling stopped: something broke", "目标调度已停止：something broke"},
		{"Focused goal: 进行中 [1.2k] - 整理仓库", "已聚焦目标：进行中 [1.2k] - 整理仓库"},
		{"Could not toggle the auditor: nope", "无法切换审计器：nope"},
		// 对话框标题
		{"Focus open goal", "聚焦进行中的目标"},
		{"Goal settings", "目标设置"},
		{"Select auditor model", "选择审计器模型"},
		{"Resume paused goal?", "恢复已暂停的目标？"},
		{"Clear goal?", "清除目标？"},
		{"Pause which open goal?", "暂停哪个进行中的目标？"},
		{"Confirm task list", "确认任务列表"},
		{"Keep current tasks", "保留当前任务"},
		{"Write your own answer...", "自行填写答案…"},
		{"Evidence for task t2", "任务 t2 的证据"},
	}
	for _, c := range cases {
		got, ok := translateText(c.in)
		if !ok {
			t.Errorf("未命中翻译: %q", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("译文不符\n  原文: %q\n  期望: %q\n  实际: %q", c.in, c.want, got)
		}
	}
	// 不该误伤的：与 goal 无关的通用文案保持原样。
	for _, keep := range []string{"Some other plugin message", "mc: 12 (3%) · idle"} {
		if got, ok := translateText(keep); ok {
			t.Errorf("无关文案被改写了: %q -> %q", keep, got)
		}
	}
}

func Test汉化状态行与通知与对话框(t *testing.T) {
	// setStatus：只有 statusKey=goal 才翻。
	raw := []byte(`{"type":"extension_ui_request","id":"1","method":"setStatus","statusKey":"goal","statusText":"goal: unfocused [3 open] - /goal-focus"}`)
	out, ok := LocalizeUIRequest(raw)
	if !ok || !strings.Contains(string(out), "未聚焦") || !strings.Contains(string(out), "3 个进行中") {
		t.Fatalf("状态行应被汉化: %s", out)
	}
	// 别的插件的状态行不动。
	other := []byte(`{"type":"extension_ui_request","id":"2","method":"setStatus","statusKey":"mc","statusText":"mc: idle"}`)
	if _, ok := LocalizeUIRequest(other); ok {
		t.Fatal("非 goal 状态行不应被改写")
	}
	// notify 固定文案。
	note := []byte(`{"type":"extension_ui_request","id":"3","method":"notify","message":"Goal paused.","notifyType":"info"}`)
	out, ok = LocalizeUIRequest(note)
	if !ok || !strings.Contains(string(out), "目标已暂停") {
		t.Fatalf("通知应被汉化: %s", out)
	}
	// 带插值的通知。
	note2 := []byte(`{"type":"extension_ui_request","id":"4","method":"notify","message":"Token budget 80% used (8000/10000 tokens). Use /goal-tweak to change or remove the budget."}`)
	out, ok = LocalizeUIRequest(note2)
	if !ok || !strings.Contains(string(out), "预算已用 80%") {
		t.Fatalf("带插值通知应被汉化: %s", out)
	}
	// 对话框标题。
	dlg := []byte(`{"type":"extension_ui_request","id":"5","method":"confirm","title":"Clear goal?","message":"目标二"}`)
	out, ok = LocalizeUIRequest(dlg)
	if !ok || !strings.Contains(string(out), "清除目标？") {
		t.Fatalf("对话框标题应被汉化: %s", out)
	}
	// 命中不到的文案原样放行。
	unknown := []byte(`{"type":"extension_ui_request","id":"6","method":"notify","message":"Some other plugin message"}`)
	if _, ok := LocalizeUIRequest(unknown); ok {
		t.Fatal("未知文案不应被改写")
	}
	// 与 goal 无关的方法不处理。
	if _, ok := LocalizeUIRequest([]byte(`{"type":"extension_ui_request","method":"setWidget"}`)); ok {
		t.Fatal("setWidget 不在处理范围")
	}
}
