package goal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 本文件把 pi-goal-x 插件产生的用户可见文案汉化。
//
// 为什么要桥侧翻译而不是改插件：插件是我们不维护的上游 npm 包，且它的文案
// 经 extension_ui_request 通道到达任意客户端；Pi 0.85.1 的 RPC 也没有给
// “插件文案 hook”留位置。桥在事件转发的唯一收口处改写这几类字段，是对所有
// 客户端都成立的一层。
//
// 覆盖面：setStatus(goal) 的状态行、notify 的提示、以及 select/confirm/input
// 对话框的标题与正文。**模型可见的提示词不在范围内**（那是注入上下文的内容，
// 不是 UI 文案）。匹配不到的文案原样放行，不会误伤别的插件。

// StatusLabel 把目标状态译成中文（面板与状态行共用）。
func StatusLabel(status string) string {
	switch status {
	case StatusActive:
		return "进行中"
	case StatusPaused:
		return "已暂停"
	case StatusBlocked:
		return "受阻"
	case StatusBudgetLimited:
		return "预算受限"
	case StatusComplete:
		return "已完成"
	default:
		if status == "" {
			return "未知"
		}
		return status
	}
}

// TaskStatusLabel 把任务状态译成中文。
func TaskStatusLabel(status string) string {
	switch status {
	case TaskComplete:
		return "已完成"
	case TaskSkipped:
		return "已跳过"
	case TaskPending:
		return "待办"
	default:
		if status == "" {
			return "待办"
		}
		return status
	}
}

// LedgerLabel 把一条账本事件译成一句中文描述。
func LedgerLabel(ev LedgerEvent) string {
	switch ev.Type {
	case "goal_created":
		return "已创建目标"
	case "goal_focused":
		return "已聚焦目标"
	case "goal_unfocused":
		return "已取消聚焦"
	case "goal_paused":
		return "已暂停目标"
	case "goal_resumed":
		return "已恢复目标"
	case "goal_tweaked":
		return "已修订目标"
	case "auditor_toggled":
		return "已切换审计器"
	case "completion_requested":
		return "已请求完成评审"
	case "audit_started":
		return "已开始独立完成评审"
	case "audit_result":
		switch ev.Verdict {
		case "approved":
			return "审计器已批准完成"
		case "disapproved":
			return "审计器要求补充工作"
		default:
			return "完成评审未能结束"
		}
	case "audit_skipped":
		return "已跳过完成评审（审计器已禁用）"
	case "goal_completed":
		return "已完成目标"
	case "goal_archived":
		return "已归档目标"
	case "goal_archive_failed":
		return "归档目标失败"
	case "goal_aborted":
		return "已中止目标"
	case "task_list_set":
		return "已设置任务列表"
	case "task_complete":
		return "已完成任务"
	case "task_skipped":
		return "已跳过任务"
	case "task_reopened":
		return "已重新打开任务"
	case "task_started":
		return "已开始任务"
	case "goal_budget_changed":
		return "已调整 Token 预算"
	case "goal_budget_limited":
		return "已达到 Token 预算"
	case "goal_budget_warning":
		return "Token 预算即将用尽"
	case "goal_stalled":
		return "目标停滞"
	case "goal_blocked":
		return "目标受阻"
	case "oracle_started":
		return "已启动阻碍顾问"
	case "oracle_result":
		return "阻碍顾问已给出建议"
	case "oracle_failed":
		return "阻碍顾问失败"
	default:
		if ev.Type == "" {
			return "事件"
		}
		return ev.Type
	}
}

// LocalizeUIRequest 改写一条 extension_ui_request 里 goal 插件的文案。
// 命中则返回改写后的 JSON 与 true；否则原样返回与 false。
func LocalizeUIRequest(raw []byte) ([]byte, bool) {
	var head struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return raw, false
	}
	switch head.Method {
	case "setStatus", "notify", "select", "confirm", "input", "editor":
	default:
		return raw, false
	}

	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return raw, false
	}
	changed := false
	rewrite := func(key string) {
		text, ok := payload[key].(string)
		if !ok || text == "" {
			return
		}
		if out, hit := translateText(text); hit {
			payload[key] = out
			changed = true
		}
	}
	// setStatus 只在状态键为 goal 时翻译，避免动其它插件的状态行。
	if head.Method == "setStatus" {
		if key, _ := payload["statusKey"].(string); key == "goal" {
			rewrite("statusText")
		}
	} else {
		rewrite("message")
		rewrite("title")
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return raw, false
	}
	return out, true
}

// OptionLabel 把对话框选项的**显示文本**译成中文，返回 (显示文本, 是否命中)。
//
// 只用于展示：调用方必须保留原始选项字符串作为回传值。插件的 select 靠
// 比较回传值判定用户选了什么，若把回传值也换成译文，选择会静默失效。
func OptionLabel(option string) (string, bool) {
	return translateText(option)
}

// translateText 应用精确表与模板规则。返回 (译文, 是否命中)。
func translateText(text string) (string, bool) {
	if v, ok := exactTranslations[text]; ok {
		return v, true
	}
	trimmed := strings.TrimSpace(text)
	if v, ok := exactTranslations[trimmed]; ok && trimmed != text {
		return v, true
	}
	for _, rule := range templateRules {
		if m := rule.re.FindStringSubmatch(text); m != nil {
			return rule.build(m), true
		}
	}
	return text, false
}

type templateRule struct {
	re    *regexp.Regexp
	build func(m []string) string
}

func rule(pattern string, build func(m []string) string) templateRule {
	return templateRule{re: regexp.MustCompile(pattern), build: build}
}

// exactTranslations 是固定文案表（无插值）。
var exactTranslations = map[string]string{
	"Goal paused.": "目标已暂停。",
	"No focused goal to toggle the auditor for.":                                 "没有聚焦的目标，无法切换审计器。",
	"This goal is complete; the auditor no longer applies.":                      "该目标已完成；审计器不再适用。",
	"Auditor enabled for this goal.":                                             "已为该目标启用审计器。",
	"Auditor disabled for this goal.":                                            "已为该目标禁用审计器。",
	"Goal focus unchanged.":                                                      "目标聚焦未变更。",
	"No goal is set.":                                                            "未设置目标。",
	"Goal is complete.":                                                          "目标已完成。",
	"Goal is already paused. Use /goal-resume to continue.":                      "目标已处于暂停状态。使用 /goal-resume 继续。",
	"No goal is focused.":                                                        "没有聚焦的目标。",
	"Goal resumed; autonomous allowance renewed.":                                "目标已恢复；自动运行额度已重置。",
	"Goal clear cancelled.":                                                      "已取消清除目标。",
	"Goal changed while confirming; nothing was cleared.":                        "确认期间目标已变更；未清除任何内容。",
	"No active draft to cancel.":                                                 "没有可取消的活动草稿。",
	"Draft cancelled; no goal was created. The execution profile is restored.":   "草稿已取消；未创建任何目标。执行配置已恢复。",
	"A draft is already active; resuming it. Use /goal-cancel to discard it.":    "已有草稿处于活动状态；正在恢复它。使用 /goal-cancel 丢弃它。",
	"Draft start cancelled; the existing draft stays active.":                    "已取消开始新草稿；现有草稿保持活动。",
	"The goal tweak draft is stale (its target goal changed); it was discarded.": "目标微调草稿已过期（其目标已变更）；已将其丢弃。",
	"Registered goals.":                                                          "",
	"Goal archived.":                                                             "目标已归档。",
	"Provider network errors persisted after all recovery attempts. The goal remains active; resume it when the provider is healthy.": "多次恢复尝试后供应商网络错误仍然存在。目标保持进行中；待供应商恢复后手动 resume。",
	"Goal checkpoint rejected: ": "目标检查点被拒绝：",
	"Goal is complete. Use /goal to draft a new one, or /goal-direct <objective> to create one immediately.":                                                                "目标已完成。用 /goal 起草一个新目标，或用 /goal-direct <objective> 立即创建。",
	"No goal is set. Use /goal to draft one, or /goal-direct <objective> to create one immediately.":                                                                        "未设置目标。用 /goal 起草一个，或用 /goal-direct <objective> 立即创建。",
	"No goal is set. Use /goal to draft one, or /goal-direct <objective> to start immediately.":                                                                             "未设置目标。用 /goal 起草一个，或用 /goal-direct <objective> 立即启动。",
	"No open goals. Use /goal to draft one, or /goal-direct <objective> to start immediately.":                                                                              "没有进行中的目标。用 /goal 起草一个，或用 /goal-direct <objective> 立即启动。",
	"A Sisyphus objective needs ordered steps with per-step done criteria. Use /sisyphus for guided drafting, or provide numbered steps (1) ..., 2) ...) in the objective.": "Sisyphus 目标需要有带每步完成判据的有序步骤。用 /sisyphus 引导式起草，或在目标描述里给出编号步骤（1) …，2) …）。",
	"goal-refresh: no changes detected — caches were already current.":                                                                                                      "goal-refresh：未检测到变更 —— 缓存已是最新。",
	"goal-recovery repair: nothing to repair.":                                                                                                                              "goal-recovery repair：无需修复。",
	"goal-recovery repair: cancelled — nothing changed.":                                                                                                                    "goal-recovery repair：已取消 —— 未做任何更改。",
	"Focus open goal":            "聚焦进行中的目标",
	"Goal settings":              "目标设置",
	"Select auditor model":       "选择审计器模型",
	"Set auditor provider/model": "设置审计器 provider/model",
	"Filter auditor models (provider/id/name; blank = all)": "筛选审计器模型（provider/id/name；留空 = 全部）",
	"Resume paused goal?":                                      "恢复已暂停的目标？",
	"Clear goal?":                                              "清除目标？",
	"Pause which open goal?":                                   "暂停哪个进行中的目标？",
	"Resume or focus open goal":                                "恢复或聚焦进行中的目标",
	"Clear which open goal?":                                   "清除哪个进行中的目标？",
	"Tweak which open goal?":                                   "微调哪个进行中的目标？",
	"Remove stale locks and refresh the pool snapshot?":        "移除陈旧锁并刷新目标池快照？",
	"Files are backed up to .pi/goals/.recovery-backup first.": "文件会先备份到 .pi/goals/.recovery-backup。",
	"Resume the existing draft":                                "恢复现有草稿",
	"Replace it with a new draft":                              "用新草稿替换",
	"Cancel":                                                   "取消",
	"Confirm task list":                                        "确认任务列表",
	"Keep current tasks":                                       "保留当前任务",
	"Replace the current task list with this structure.":       "用此结构替换当前任务列表。",
	"Leave the existing task list unchanged.":                  "保持现有任务列表不变。",
	"Enabled — require independent approval":                   "启用 —— 需要独立批准",
	"Disabled — skip the completion audit":                     "禁用 —— 跳过完成审计",
	"Write your own answer...":                                 "自行填写答案…",
	"Continue working":                                         "继续工作",
	"Mark complete without audit":                              "不经审计标记完成",
	"Bypass the auditor and mark the goal complete now.":       "绕过审计器并立即将目标标记为完成。",
	"Resume work on the goal. It stays active; the audit will not run this turn.": "继续该目标的工作。目标保持进行中；本轮不会运行审计。",
	"Goal owned by another session. Use /goal-resume to take ownership.":          "目标归属另一会话。使用 /goal-resume 取得所有权。",
	"No objective provided. Use ": "未提供目标描述。使用 ",
}

// templateRules 是有插值的文案规则，按顺序匹配（先具体后宽泛）。
var templateRules = []templateRule{
	// setStatus：goal: unfocused [N open] - /goal-focus
	rule(`^goal: unfocused \[(\d+) open\] - /goal-focus$`, func(m []string) string {
		return fmt.Sprintf("目标：未聚焦（%s 个进行中）· /goal-focus", m[1])
	}),
	// 未聚焦目标汇总
	rule(`^No goal is focused in this session\. (\d+) open goals? exist in the goal pool\..*$`, func(m []string) string {
		return fmt.Sprintf("本会话未聚焦任何目标。目标池中有 %s 个进行中的目标。开始目标工作前，请用 /goal-focus 选择本会话聚焦项。", m[1])
	}),
	// notify：归档（可能带文件行）
	rule(`(?s)^Goal archived\.\nFile: (.+)$`, func(m []string) string {
		return "目标已归档。\n文件：" + m[1]
	}),
	rule(`^Failed to archive completed goal: (.+)\. The complete record remains at (.+)\.$`, func(m []string) string {
		return fmt.Sprintf("归档已完成目标失败：%s。完整记录仍保留在 %s。", m[1], m[2])
	}),
	// Token 预算提醒
	rule(`^Token budget (\d+)% used \((\d+)/(\d+) tokens\)\. Use /goal-tweak to change or remove the budget\.$`, func(m []string) string {
		return fmt.Sprintf("Token 预算已用 %s%%（%s/%s tokens）。可用 /goal-tweak 修改或移除预算。", m[1], m[2], m[3])
	}),
	// 停滞
	rule(`^Goal stalled: no activity for (\d+) minutes?\.$`, func(m []string) string {
		return fmt.Sprintf("目标停滞：已 %s 分钟无活动。", m[1])
	}),
	// 网络错误重试
	rule(`^Provider network error\. Retrying the goal in (\d+)s \((.+)\)\.$`, func(m []string) string {
		return fmt.Sprintf("供应商网络错误。将在 %s 秒后重试该目标（%s）。", m[1], m[2])
	}),
	// 调度停止 / 检查点拒绝
	rule(`^Goal scheduling stopped: (.+)$`, func(m []string) string {
		return "目标调度已停止：" + m[1]
	}),
	rule(`^Goal checkpoint rejected: (.+)$`, func(m []string) string {
		return "目标检查点被拒绝：" + m[1]
	}),
	// 聚焦 / 取消聚焦
	rule(`^Focused goal: (.+)$`, func(m []string) string {
		return "已聚焦目标：" + m[1]
	}),
	rule(`^Goal unfocused for this session\. It remains open in \.pi/goals: (.+)$`, func(m []string) string {
		return "本会话已取消目标聚焦。它仍在 .pi/goals 中保持进行中：" + m[1]
	}),
	// 设置相关
	rule(`^(.+) is read-only: overridden by the (.+) env var\.$`, func(m []string) string {
		return fmt.Sprintf("%s 为只读：被环境变量 %s 覆盖。", m[1], m[2])
	}),
	rule(`^Settings change failed: (.+)$`, func(m []string) string {
		return "设置更改失败：" + m[1]
	}),
	rule(`^Could not start (.+): (.+)$`, func(m []string) string {
		return fmt.Sprintf("无法启动%s：%s", m[1], m[2])
	}),
	rule(`^Could not toggle the auditor: (.+)$`, func(m []string) string {
		return "无法切换审计器：" + m[1]
	}),
	// 起草
	rule(`^A (.+) is already active$`, func(m []string) string {
		return fmt.Sprintf("已有一个%s处于活动状态", m[1])
	}),
	rule(`^(.+) started(.*)\. The agent will clarify, propose a goal and tasks where useful, then ask you to confirm\.$`, func(m []string) string {
		return fmt.Sprintf("%s已开始%s。代理会先澄清、在有用时提议目标与任务，然后请你确认。", m[1], m[2])
	}),
	rule(`^Replacing the active draft with a new (.+)\.$`, func(m []string) string {
		return fmt.Sprintf("正在用新的%s替换活动草稿。", m[1])
	}),
	// 设置项对话框
	rule(`^Set (.+)$`, func(m []string) string {
		return "设置 " + m[1]
	}),
	rule(`^(.+) \(([a-z]+)\)$`, func(m []string) string {
		return fmt.Sprintf("%s（%s）", m[1], m[2])
	}),
	rule(`^Completion auditor \(currently (.+)\)$`, func(m []string) string {
		return fmt.Sprintf("完成审计器（当前 %s）", auditStateCN(m[1]))
	}),
	// goal-refresh
	rule(`^goal-refresh: re-read caches from disk — (\d+) change\(s\):\n?(.*)$`, func(m []string) string {
		return fmt.Sprintf("goal-refresh：已从磁盘重读缓存 —— %s 处变更：\n%s", m[1], m[2])
	}),
	// goal-recovery repair
	rule(`^goal-recovery repair: (\d+) operation\(s\) applied\.\n?(.*)$`, func(m []string) string {
		return fmt.Sprintf("goal-recovery repair：已应用 %s 项操作。\n%s", m[1], m[2])
	}),
	// 任务确认对话框标题
	rule(`(?s)^Task list confirmation\n\n(.*)$`, func(m []string) string {
		return "任务列表确认\n\n" + m[1]
	}),
	// 证据输入
	rule(`^Evidence for task (.+)$`, func(m []string) string {
		return "任务 " + m[1] + " 的证据"
	}),
	// 清空命令
	rule(`^Run /goal-clear in an interactive session to confirm clearing: (.+)$`, func(m []string) string {
		return "请在交互式会话中运行 /goal-clear 以确认清除：" + m[1]
	}),
	rule(`^No open goals\. Use /goal to draft one.*$`, func(m []string) string {
		return "没有进行中的目标。用 /goal 起草一个，或用 /goal-direct <objective> 立即启动。"
	}),
	// 目标选择框的一行：`{marker} {id} | {statusLabel} | {mode} | {title} {path}`
	// （见 pi-goal-x goal-pool.ts 的 goalSelectorLabel）。marker/id/标题原样保留，
	// 只把状态与模式译成中文；这串只作显示，回传值仍是原文。
	rule(`^(.*?) \| ((?:sisyphus )?(?:running|paused \(agent\)|paused|blocked|budget limited|complete|active)) \| (sisyphus|goal) \| (.*)$`, func(m []string) string {
		return m[1] + " | " + goalStatusCN(m[2]) + " | " + goalModeCN(m[3]) + " | " + m[4]
	}),
}

// goalStatusCN 译目标状态标签（statusLabel 的取值形态）。
func goalStatusCN(label string) string {
	prefix := ""
	rest := label
	if strings.HasPrefix(label, "sisyphus ") {
		prefix = "有序步骤 "
		rest = strings.TrimPrefix(label, "sisyphus ")
	}
	switch rest {
	case "running":
		return prefix + "进行中"
	case "paused (agent)":
		return prefix + "已暂停（代理）"
	case "paused":
		return prefix + "已暂停"
	case "blocked":
		return prefix + "受阻"
	case "budget limited":
		return prefix + "预算受限"
	case "complete":
		return prefix + "已完成"
	case "active":
		return prefix + "进行中"
	default:
		return label
	}
}

// goalModeCN 译目标模式。
func goalModeCN(mode string) string {
	if mode == "sisyphus" {
		return "有序步骤"
	}
	return "普通目标"
}

func auditStateCN(v string) string {
	switch v {
	case "enabled":
		return "已启用"
	case "disabled":
		return "已禁用"
	default:
		return v
	}
}
