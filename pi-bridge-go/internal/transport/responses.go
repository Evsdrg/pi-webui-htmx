package transport

import (
	"encoding/json"
	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/workspace"
)

// 命令回执的具名类型。
//
// 为什么不用 map[string]any：这些 map 就是**线上格式**，是前端按名取值、
// 也是 api/v1/protocol.md 描述的东西，但它们以前散在三十多个 return 上，
// 键名写错不会被任何东西发现——map 的键是字符串，编译器只看类型。
// 具名类型把键名收进 json 标签，写错标签至少是一个能被 grep 与测试
// 核对的点，而且同一形状的多个方法现在**共用同一个类型**，
// 想改就必须一次改到全部（下面每个类型都注明了哪些方法用它）。
//
// 形状本身由 responses_test.go 钉住：它按方法列出期望的键集合，
// 与 protocol.md 的描述一一对应。
//
// 这里刻意不引入「统一响应信封」：协议的 response 已经有 kind/requestId/ok/
// data 外壳（protocol.Reply），data 里再包一层只会让前端多剥一层。

// queueModeReply 是 session.set_queue_mode 的回执。
type queueModeReply struct {
	Kind string `json:"kind"`
	Mode string `json:"mode"`
}

// queuedReply 表示消息已排入队列：session.steer、session.follow_up。
//
// 它**不代表任务已完成**——Pi 接受排队与任务结束是两件事（#258）。
type queuedReply struct {
	Queued bool `json:"queued"`
}

// enabledReply 是自动压缩/自动重试开关的回执：
// session.set_auto_compaction、session.set_auto_retry。
type enabledReply struct {
	Enabled bool `json:"enabled"`
}

// abortedReply 是中止类命令的回执：session.abort_retry、session.abort_bash。
type abortedReply struct {
	Aborted bool `json:"aborted"`
}

// acceptedReply 是 session.prompt 的回执。
// 与 queuedReply 同理：accepted 表示 Pi 已接受，不是已经答完。
type acceptedReply struct {
	Accepted bool `json:"accepted"`
}

// clearedQueueReply 是 session.abort 的回执。
// clearedQueue 是 Pi 原样返回的队列快照（已清掉的部分），
// 桥不解释它的结构，因此保留为原始 JSON。
type clearedQueueReply struct {
	ClearedQueue json.RawMessage `json:"clearedQueue"`
}

// stoppedReply 是 session.stop 的回执。
type stoppedReply struct {
	Stopped bool `json:"stopped"`
}

// levelReply 是思考等级设置/轮换的回执：
// session.set_thinking、session.cycle_thinking。返回的是生效后的等级。
type levelReply struct {
	Level string `json:"level"`
}

// renamedReply 是 session.set_name 的回执。
type renamedReply struct {
	Renamed bool `json:"renamed"`
}

// textReply 是「取回一段文本」的回执，当前用于 session.last_assistant。
type textReply struct {
	Text string `json:"text"`
}

// textTruncatedReply 是可能被截断的文本回执，当前用于 session.bash_output。
type textTruncatedReply struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// sessionIDReply 是新会话身份类命令的回执：
// session.new、session.switch、session.clone。
// 三者都必须返回**新分配或切换后的**会话 ID，共用类型保证它们一致。
type sessionIDReply struct {
	SessionID string `json:"sessionId"`
}

// deleteReply 是 sessions.delete 的回执。
//
// 内嵌 DeleteResult 而不是重写一遍字段：停掉运行中会话时以前会另拼一个
// 含同名字段的 map，一旦 DeleteResult 增字段，两条路径就会给出不同形状。
type deleteReply struct {
	sessions.DeleteResult
	// StoppedWorker 只在本次删除连带停掉运行中的会话时为真；
	// 为假时省略，与「没有停过东西」区分开。
	StoppedWorker bool `json:"stoppedWorker,omitempty"`
}

// exportReply 是 session.export_html 的回执。路径已由 safeExportName
// 与 exportDir 约束，调用方不能借它读任意文件。
type exportReply struct {
	Path string `json:"path"`
}

// writtenReply 表示字节已写入：config.models.write、terminal.input。
type writtenReply struct {
	Written bool `json:"written"`
}

// resizedReply / closedReply 是终端控制回执。
type resizedReply struct {
	Resized bool `json:"resized"`
}

type closedReply struct {
	Closed bool `json:"closed"`
}

// answeredReply 是 session.ui_response 的回执：扩展对话已答复。
type answeredReply struct {
	Answered bool `json:"answered"`
}

// modelsReply 是模型清单类回执：config.models.discover（供应商返回的）、
// config.catalog（models.dev 目录）。
//
// 两者内容来自不同来源但形状相同，共用类型是有意的：前端读的是同一个键。
type modelsReply struct {
	Models []management.DiscoveredModel `json:"models"`
}

// packagesReply 是 config.packages 的回执。
type packagesReply struct {
	Packages []management.PackageInfo `json:"packages"`
}

// terminalOpenedReply 是 terminal.open 的回执，前端用它建立终端视图。
type terminalOpenedReply struct {
	TerminalID string `json:"terminalId"`
	Cwd        string `json:"cwd"`
	PID        int    `json:"pid"`
	Cols       uint16 `json:"cols"`
	Rows       uint16 `json:"rows"`
}

// terminalsReply 是 terminal.list 的回执。
type terminalsReply struct {
	Terminals []terminal.Info `json:"terminals"`
}

// entriesReply 是 files.list 的回执：受管目录下的一层目录项。
type entriesReply struct {
	Entries   []workspace.Entry `json:"entries"`
	Truncated bool              `json:"truncated"`
}

// fileTextReply 是 files.read 的回执。
// Size 是文件总字节数，Text 可能因截断而更短。
type fileTextReply struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
}

// fileImageReply 是 files.image 的回执：图片以 base64 放在 data 里。
type fileImageReply struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
	Size int    `json:"size"`
}

// rootsReply 是 files.roots 的回执：允许浏览的工作区根。
type rootsReply struct {
	Roots []string `json:"roots"`
}

// diffReply 是 git.diff 的回执。
type diffReply struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

// dialogsReply 是 session.pending_dialogs 的回执：
// dialogs 是 Pi 的原始载荷（HTTP 端点据此渲染对话框），ids 是待回复的 ID 清单。
type dialogsReply struct {
	Dialogs []json.RawMessage `json:"dialogs"`
	IDs     []string          `json:"ids"`
}

// extStatusReply 是 session.ext_status 的回执：插件状态行快照，
// 以及这份快照所属的 worker epoch（B36）。
type extStatusReply struct {
	Epoch    string            `json:"epoch"`
	Statuses map[string]string `json:"statuses"`
}

// 事件载荷（WS 的 kind=event / control 帧的 data）。
//
// 与回执同理：键名是线上格式，前端按名读取。这里只列桥自己构造的事件；
// Pi 的事件（agent_start、message_update 等）是原样透传的原始 JSON，
// 结构由 Pi 决定，桥不解释也不重写。

// terminalClosedEvent 在终端退出时发给发起它的连接。
type terminalClosedEvent struct {
	TerminalID string `json:"terminalId"`
}

// terminalOutputEvent 推送终端输出。Data 是终端字节流，
// 已按 UTF-8 转换——二进制协议不要用终端（见文档「终端」一节）。
type terminalOutputEvent struct {
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
}
