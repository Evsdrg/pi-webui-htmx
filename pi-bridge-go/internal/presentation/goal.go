package presentation

import (
	"fmt"
	"strings"
	"time"

	"pi-bridge-go/internal/goal"
)

// GoalPanel 是「目标」面板的数据。所有面向用户的标签都在这里算好中文，
// 模板只负责摆版面。
type GoalPanel struct {
	Available bool
	Notice    string
	Root      string
	Focused   *GoalCard
	Others    []GoalCard
	Ledger    []GoalEventRow
}

// GoalCard 是一个目标的完整卡片。
type GoalCard struct {
	ID              string
	Objective       string
	StatusLabel     string
	StatusClass     string
	Mode            string
	AutoContinue    bool
	Usage           string
	Budget          string
	TasksDone       int
	TasksTotal      int
	CurrentTask     string
	Verification    string
	PauseReason     string
	SuggestedAction string
	File            string
	Archived        bool
	BlockCompletion bool
	Tasks           []GoalTaskRow
}

// GoalTaskRow 是任务树的一行。
type GoalTaskRow struct {
	ID          string
	Title       string
	StatusLabel string
	StatusClass string
	Depth       int
	Note        string
}

// GoalEventRow 是账本里的一行。
type GoalEventRow struct {
	Label  string
	Detail string
	When   string
}

const (
	goalObjectiveMax = 4000
	goalLedgerRows   = 12
)

// GoalPanelFrom 把只读视图转成面板数据（补上中文标签与格式化字段）。
func GoalPanelFrom(v goal.View) GoalPanel {
	panel := GoalPanel{Available: v.Available, Notice: v.Notice, Root: v.Root}
	if v.Focused != nil {
		card := goalCardFromGoal(*v.Focused)
		panel.Focused = &card
	}
	for _, g := range v.Goals {
		if v.Focused != nil && g.ID == v.Focused.ID {
			continue
		}
		panel.Others = append(panel.Others, goalCardFromGoal(g))
	}
	for _, ev := range v.Ledger {
		panel.Ledger = append(panel.Ledger, goalEventRow(ev))
	}
	return panel
}

func goalCardFromGoal(g goal.Goal) GoalCard {
	card := GoalCard{
		ID:           g.ID,
		Objective:    truncateRunes(g.Objective, goalObjectiveMax),
		StatusLabel:  goal.StatusLabel(g.Status),
		StatusClass:  goalTagClass(g.Status),
		Mode:         goalMode(g.Sisyphus),
		AutoContinue: g.AutoContinue,
		File:         g.File,
		Archived:     g.Archived,
	}
	card.Usage = fmt.Sprintf("用时 %s · %s tokens", formatWorkDuration(g.Usage.ActiveSeconds), formatTokens(g.Usage.TokensUsed))
	if g.TokenBudget != nil {
		card.Budget = fmt.Sprintf("预算 %s / %s", formatTokens(g.Usage.TokensUsed), formatTokens(*g.TokenBudget))
	}
	card.TasksTotal, card.TasksDone = g.TaskCounts()
	card.BlockCompletion = g.BlockCompletion()
	if g.CurrentTaskID != "" {
		card.CurrentTask = g.CurrentTaskID
	}
	card.Verification = strings.TrimSpace(g.VerificationContract)
	card.PauseReason = strings.TrimSpace(g.PauseReason)
	card.SuggestedAction = strings.TrimSpace(g.PauseSuggestedAction)
	card.Tasks = goalTaskRows(g.Tasks(), 0)
	return card
}

func goalTaskRows(tasks []goal.Task, depth int) []GoalTaskRow {
	rows := make([]GoalTaskRow, 0, len(tasks))
	for _, t := range tasks {
		note := ""
		switch {
		case t.Status == goal.TaskComplete && t.Evidence != "":
			note = "证据：" + t.Evidence
		case t.Status == goal.TaskSkipped && t.SkipReason != "":
			note = "跳过理由：" + t.SkipReason
		case t.Status == goal.TaskPending && t.VerificationContract != "":
			note = "验收：" + t.VerificationContract
		}
		rows = append(rows, GoalTaskRow{
			ID:          t.ID,
			Title:       t.Title,
			StatusLabel: goal.TaskStatusLabel(t.Status),
			StatusClass: goalTaskTagClass(t.Status),
			Depth:       depth,
			Note:        note,
		})
		rows = append(rows, goalTaskRows(t.Subtasks, depth+1)...)
		rows = append(rows, goalTaskRows(t.LightweightSubtasks, depth+1)...)
	}
	return rows
}

func goalEventRow(ev goal.LedgerEvent) GoalEventRow {
	row := GoalEventRow{Label: goal.LedgerLabel(ev), When: shortTime(ev.At)}
	switch {
	case ev.Reason != "":
		row.Detail = ev.Reason
	case ev.Report != "":
		row.Detail = summarize(ev.Report, 160)
	case ev.Evidence != "":
		row.Detail = ev.Evidence
	case ev.TaskID != "":
		row.Detail = ev.TaskID
	}
	return row
}

// goalTagClass 给状态配一个既有的标签配色类（复用 .tag-* 家族，不新增 CSS）。
func goalTagClass(status string) string {
	switch status {
	case goal.StatusComplete:
		return "tag-add"
	case goal.StatusBlocked:
		return "tag-err"
	case goal.StatusBudgetLimited, goal.StatusPaused:
		return "tag-warn"
	default:
		return ""
	}
}

func goalTaskTagClass(status string) string {
	switch status {
	case goal.TaskComplete:
		return "tag-add"
	case goal.TaskSkipped:
		return "tag-warn"
	default:
		return ""
	}
}

func goalMode(sisyphus bool) string {
	if sisyphus {
		return "有序步骤"
	}
	return "普通目标"
}

// formatTokens 把 token 数压成短标签（1.2k / 3.4M）。
func formatTokens(value int) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func shortTime(iso string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(iso))
	if err != nil {
		return strings.TrimSpace(iso)
	}
	return t.Local().Format("01-02 15:04")
}

func summarize(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, max)
}

func truncateRunes(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

// RenderGoal 渲染目标面板片段。
func (r *Renderer) RenderGoal(panel GoalPanel) (string, error) {
	return r.execute("goal.html", panel)
}
