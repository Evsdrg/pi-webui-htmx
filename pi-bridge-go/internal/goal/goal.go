// Package goal 只读读取 pi-goal-x 插件在工作区里落的目标数据，供 WebUI 展示。
//
// 为什么只读、为什么走文件：该插件把自己的权威状态放在
// <cwd>/.pi/goals/*.md（文件头是一段 JSON 元数据，其后是可编辑正文）与
// <cwd>/.pi/goals/goal_events.jsonl（append-only 账本）里，不走 Pi 的 UI 通道。
// 而 Pi 0.85.1 的 RPC 模式会忽略插件注册的 setWidget(key, factory) 工厂函数
// （rpc-mode.js 只接受字符串数组），所以插件自带的仪表盘在 RPC 客户端上根本
// 到不了——和 magic-context 的 todo 面板是同一个坑。桥按需只读这些文件、
// 自己渲染，是对任何客户端都成立的路径。
//
// 边界：本包**只读**，绝不写目标文件、账本或设置；也不把它们复制进桥的存储。
// 目标文件的权威仍然在磁盘上。
package goal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 目标状态取值，与 goal-record.ts 的 GoalStatus 一致。
const (
	StatusActive        = "active"
	StatusPaused        = "paused"
	StatusBlocked       = "blocked"
	StatusBudgetLimited = "budget_limited"
	StatusComplete      = "complete"
)

// 任务状态取值，与 goal-record.ts 的 TaskStatus 一致。
const (
	TaskPending  = "pending"
	TaskComplete = "complete"
	TaskSkipped  = "skipped"
)

// DefaultLedgerLimit 是面板默认展示的账本条数（新的在前）。
const DefaultLedgerLimit = 12

// Task 是一个任务（可递归含子任务）。
type Task struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	Evidence             string `json:"evidence,omitempty"`
	SkipReason           string `json:"skipReason,omitempty"`
	VerificationContract string `json:"verificationContract,omitempty"`
	Subtasks             []Task `json:"subtasks,omitempty"`
	LightweightSubtasks  []Task `json:"lightweightSubtasks,omitempty"`
}

// Goal 是一个目标。字段取自目标文件头部的 JSON 元数据（GoalRecord 的子集）。
type Goal struct {
	ID                   string `json:"id"`
	Objective            string `json:"objective"`
	Status               string `json:"status"`
	AutoContinue         bool   `json:"autoContinue"`
	Sisyphus             bool   `json:"sisyphus"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
	StopReason           string `json:"stopReason,omitempty"`
	PauseReason          string `json:"pauseReason,omitempty"`
	PauseSuggestedAction string `json:"pauseSuggestedAction,omitempty"`
	TokenBudget          *int   `json:"tokenBudget,omitempty"`
	CurrentTaskID        string `json:"currentTaskId,omitempty"`
	VerificationContract string `json:"verificationContract,omitempty"`
	Usage                struct {
		TokensUsed    int `json:"tokensUsed"`
		ActiveSeconds int `json:"activeSeconds"`
	} `json:"usage"`
	TaskList *struct {
		Tasks           []Task `json:"tasks"`
		BlockCompletion bool   `json:"blockCompletion"`
	} `json:"taskList,omitempty"`

	// 以下是桥自己推导的只读字段，不来自目标文件。
	Archived bool `json:"-"`
	// File 是目标文件路径（用于“文件”一行；只暴露给桥内部与模板，不落盘）。
	File string `json:"-"`
}

// Tasks 返回顶层任务（可能为 nil）。
func (g Goal) Tasks() []Task {
	if g.TaskList == nil {
		return nil
	}
	return g.TaskList.Tasks
}

// BlockCompletion 表示“有待办任务时不允许完成”。
func (g Goal) BlockCompletion() bool {
	return g.TaskList != nil && g.TaskList.BlockCompletion
}

// TaskCounts 统计任务（含子任务）的完成情况。
func (g Goal) TaskCounts() (total, done int) {
	var walk func(tasks []Task)
	walk = func(tasks []Task) {
		for _, t := range tasks {
			total++
			if t.Status == TaskComplete || t.Status == TaskSkipped {
				done++
			}
			walk(t.Subtasks)
			walk(t.LightweightSubtasks)
		}
	}
	walk(g.Tasks())
	return total, done
}

// LedgerEvent 是账本里的一条事件（只保留面板要用的字段）。
type LedgerEvent struct {
	Type     string `json:"type"`
	At       string `json:"at"`
	GoalID   string `json:"goalId,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Report   string `json:"report,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// View 是渲染一个会话目标面板所需的全部数据。
type View struct {
	// Available 表示本工作区是否真的存在 goals 目录（没装插件或从未用过）。
	Available bool
	// Root 是解析出的 goals 目录（用于“文件”一行）。
	Root string
	// FocusedGoalID 是本会话聚焦的目标 id（来自会话 JSONL 里的 pi-goal-focus）。
	FocusedGoalID string
	// Goals 是与本工作区相关的目标：进行中的在前，其后是聚焦目标（即使已归档）。
	Goals []Goal
	// Focused 是当前聚焦的目标（可能为空）。
	Focused *Goal
	// Ledger 是聚焦目标最近的账本事件（新的在前）。
	Ledger []LedgerEvent
	// Notice 是降级/失败说明（如目录不存在、文件解析失败），供面板如实标注。
	Notice string
}

// RootFor 解析某工作区的 goals 目录。
//
// 顺序与插件一致（goal-root.ts）：<cwd>/.pi/goals 是默认；若项目或全局
// 设置文件里给了 goalsRoot（或环境变量 PI_GOAL_ROOT），则用那个。设置文件
// 里的相对路径按 cwd 解析，`~`/`~/x` 展开为用户主目录。
func RootFor(cwd, agentDir string) string {
	if v := os.Getenv("PI_GOAL_ROOT"); v != "" {
		if p, ok := expandRoot(cwd, v); ok {
			return p
		}
	}
	for _, file := range []string{
		filepath.Join(cwd, ".pi", "pi-goal-x-settings.json"),
		filepath.Join(agentDir, "pi-goal-x-settings.json"),
	} {
		if v, ok := readGoalsRoot(file); ok {
			if p, ok := expandRoot(cwd, v); ok {
				return p
			}
		}
	}
	return filepath.Join(cwd, ".pi", "goals")
}

func expandRoot(cwd, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		value = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~"), "/"))
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(cwd, value)
	}
	clean := filepath.Clean(value)
	if strings.ContainsRune(clean, 0) {
		return "", false
	}
	return clean, true
}

func readGoalsRoot(path string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var doc struct {
		GoalsRoot string `json:"goalsRoot"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return "", false
	}
	return doc.GoalsRoot, doc.GoalsRoot != ""
}

// Load 读取 root 下的目标与账本，并结合本会话的聚焦目标组装视图。
// 任何读取失败都退化成“带说明的空视图”，不返回错误——面板不该因为磁盘问题而空白失败。
func Load(root, focusedGoalID string, ledgerLimit int) View {
	view := View{Root: root, FocusedGoalID: focusedGoalID}

	entries, err := os.ReadDir(root)
	if err != nil {
		view.Notice = "本工作区还没有目标（未安装 pi-goal-x 或从未创建过）。"
		return view
	}
	view.Available = true

	var open, archived []Goal
	var malformed int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "active_goal_") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if g, ok := parseGoalFile(filepath.Join(root, entry.Name())); ok {
			open = append(open, g)
		} else {
			malformed++
		}
	}
	if arch, err := os.ReadDir(filepath.Join(root, "archived")); err == nil {
		for _, entry := range arch {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			if g, ok := parseGoalFile(filepath.Join(root, "archived", entry.Name())); ok {
				g.Archived = true
				archived = append(archived, g)
			}
		}
	}

	// 进行中的目标按更新时间倒序。
	sort.SliceStable(open, func(i, j int) bool { return open[i].UpdatedAt > open[j].UpdatedAt })
	view.Goals = open

	// 聚焦目标可能仍在进行中，也可能已经归档（完成后聚焦未清）。
	if focusedGoalID != "" {
		for i := range open {
			if open[i].ID == focusedGoalID {
				view.Focused = &view.Goals[i]
				break
			}
		}
		if view.Focused == nil {
			for i := range archived {
				if archived[i].ID == focusedGoalID {
					view.Focused = &archived[i]
					break
				}
			}
		}
	}
	if malformed > 0 {
		view.Notice = "有目标文件无法解析，已跳过；面板只显示可读的部分。"
	}
	if ledgerLimit > 0 && view.Focused != nil {
		view.Ledger = readLedger(filepath.Join(root, "goal_events.jsonl"), view.Focused.ID, ledgerLimit)
	}
	return view
}

// parseGoalFile 解析目标文件：文件头是一段完整 JSON，其后是 Markdown 正文。
// JSON 之后的内容里，objective 从“# Goal Prompt”段重新提取（与插件一致），
// 因为这个字段允许用户在文件里手改。
func parseGoalFile(path string) (Goal, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Goal{}, false
	}
	// 目标文件不允许是符号链接（与插件一致：符号链接直接跳过）。
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return Goal{}, false
	}
	end := findJSONObjectEnd(string(body))
	if end < 0 {
		return Goal{}, false
	}
	var g Goal
	if json.Unmarshal(body[:end+1], &g) != nil || g.ID == "" {
		return Goal{}, false
	}
	if obj := extractObjective(string(body[end+1:])); obj != "" {
		g.Objective = obj
	}
	g.File = path
	return g, true
}

// findJSONObjectEnd 找到第一个平衡的顶层 JSON 对象的结束下标（-1 表示没有）。
// 逐字符扫描、跟踪字符串与转义，避免正文里的 '}' 干扰。
func findJSONObjectEnd(content string) int {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// extractObjective 从“# Goal Prompt”标题到“## Progress”之间的正文提取 objective。
func extractObjective(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "# Goal Prompt" {
			start = i
			break
		}
	}
	if start < 0 {
		return strings.TrimSpace(body)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "## Progress" {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start+1:end], "\n"))
}

// readLedger 取某目标最近的账本事件（新的在前，最多 limit 条）。
// 逐行扫描、容忍坏行：账本可能很大，但每行都是独立 JSON。
func readLedger(path, goalID string, limit int) []LedgerEvent {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	// 只留最近 limit 条，环形覆盖，避免为一份大账本把全部事件读进内存。
	buf := make([]LedgerEvent, 0, limit)
	count := 0
	dec := json.NewDecoder(file)
	for dec.More() {
		var ev LedgerEvent
		if dec.Decode(&ev) != nil {
			break // 坏行之后不再继续解析（保持有界）。
		}
		if ev.GoalID != "" && ev.GoalID != goalID {
			continue
		}
		if len(buf) < limit {
			buf = append(buf, ev)
		} else {
			copy(buf, buf[1:])
			buf[len(buf)-1] = ev
		}
		count++
	}
	// 反转为“新的在前”。
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return buf
}
