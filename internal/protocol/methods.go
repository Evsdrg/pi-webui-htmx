// Package protocol 定义桥与前端之间的线上协议。
// 本文件是命令执行策略的唯一事实来源：执行类别、是否需要在派发前
// 可靠写 intent、是否属于必须插队的控制命令。
package protocol

// Class 是命令的执行类别，决定它走哪套预算与持久化策略。
// 新增方法必须在这里声明类别，静态测试会检查没有遗漏。
type Class string

const (
	// ClassRead 是本地只读，不触碰 Pi 进程。
	ClassRead Class = "read"
	// ClassWorkerRead 读取当前 worker 的状态，可能触发启动。
	ClassWorkerRead Class = "worker_read"
	// ClassDiskRead 读受管目录或 Git 元数据。
	ClassDiskRead Class = "disk_read"
	// ClassNetwork 向已审核的 origin 发出站请求。
	ClassNetwork Class = "network"
	// ClassConnection 是订阅类连接资源。
	ClassConnection Class = "connection"
	// ClassLifecycle 申请或释放 Pi 进程。
	ClassLifecycle Class = "lifecycle"
	// ClassExec 是一次性执行，接受与完成必须分开。
	ClassExec Class = "exec"
	// ClassState 变更 worker 的运行状态。
	ClassState Class = "state"
	// ClassIdentity 改变会话身份，必须走身份事务。
	ClassIdentity Class = "identity"
	// ClassConfig 持久修改桥本地的配置文件。
	ClassConfig Class = "config"
	// ClassDelete 持久删除会话。
	ClassDelete Class = "delete"
	// ClassTerminal 创建终端进程。
	ClassTerminal Class = "terminal"
	// ClassInput 是临时输入，不逐批持久化。
	ClassInput Class = "input"
	// ClassControl 是取消/停止类，必须能插到普通写队列前面。
	ClassControl Class = "control"
	// ClassDialog 回复扩展对话。
	ClassDialog Class = "dialog"
	// ClassExport 生成有界产物。
	ClassExport Class = "export"
)

// Spec 描述一个命令的执行策略。
type Spec struct {
	Method string
	Class  Class
	// NeedsIntent 表示派发前必须可靠写 intent。
	// 缺了它，桥在「Pi 已接受命令」与「回执落盘」之间崩溃时，
	// 重启后同一 requestId 没有记录，无法判断是否执行过。
	NeedsIntent bool
	// Urgent 表示控制命令：不能排在普通写队列后面等待。
	Urgent bool
}

// specs 是全部命令的执行策略。与 SupportedMethods 一一对应，
// 由 methods_test.go 静态核对，防止新增方法漏列。
var specs = map[string]Spec{
	// 本地只读。
	"worker.list":       {Method: "worker.list", Class: ClassRead},
	"config.models":     {Method: "config.models", Class: ClassRead},
	"config.models.raw": {Method: "config.models.raw", Class: ClassRead},
	"config.settings":   {Method: "config.settings", Class: ClassRead},
	"config.trust":      {Method: "config.trust", Class: ClassRead},
	"terminal.list":     {Method: "terminal.list", Class: ClassRead},

	// 读取当前 worker 状态。
	"session.state":           {Method: "session.state", Class: ClassWorkerRead},
	"session.models":          {Method: "session.models", Class: ClassWorkerRead},
	"session.thinking_levels": {Method: "session.thinking_levels", Class: ClassWorkerRead},
	"session.stats":           {Method: "session.stats", Class: ClassWorkerRead},
	"session.last_assistant":  {Method: "session.last_assistant", Class: ClassWorkerRead},
	"session.commands":        {Method: "session.commands", Class: ClassWorkerRead},
	"session.tree":            {Method: "session.tree", Class: ClassWorkerRead},
	"session.fork_messages":   {Method: "session.fork_messages", Class: ClassWorkerRead},
	"session.entries":         {Method: "session.entries", Class: ClassWorkerRead},
	"session.bash_output":     {Method: "session.bash_output", Class: ClassWorkerRead},
	"session.pending_dialogs": {Method: "session.pending_dialogs", Class: ClassWorkerRead},

	// 文件与目录只读。
	"sessions.search": {Method: "sessions.search", Class: ClassDiskRead},
	"files.list":      {Method: "files.list", Class: ClassDiskRead},
	"files.index":     {Method: "files.index", Class: ClassDiskRead},
	"files.stat":      {Method: "files.stat", Class: ClassDiskRead},
	"files.read":      {Method: "files.read", Class: ClassDiskRead},
	"files.image":     {Method: "files.image", Class: ClassDiskRead},
	"files.roots":     {Method: "files.roots", Class: ClassDiskRead},
	"git.status":      {Method: "git.status", Class: ClassDiskRead},
	"git.diff":        {Method: "git.diff", Class: ClassDiskRead},

	// 联网只读。
	"config.models.discover": {Method: "config.models.discover", Class: ClassNetwork},
	"config.models.test":     {Method: "config.models.test", Class: ClassNetwork},
	"config.catalog":         {Method: "config.catalog", Class: ClassNetwork},
	"config.packages":        {Method: "config.packages", Class: ClassNetwork},

	// 连接资源。
	"session.subscribe":   {Method: "session.subscribe", Class: ClassConnection},
	"session.unsubscribe": {Method: "session.unsubscribe", Class: ClassConnection},

	// 生命周期。
	"session.start": {Method: "session.start", Class: ClassLifecycle, NeedsIntent: true},
	"session.stop":  {Method: "session.stop", Class: ClassControl, Urgent: true},

	// 一次性执行。
	"session.prompt":    {Method: "session.prompt", Class: ClassExec, NeedsIntent: true},
	"session.steer":     {Method: "session.steer", Class: ClassExec, NeedsIntent: true},
	"session.follow_up": {Method: "session.follow_up", Class: ClassExec, NeedsIntent: true},
	"session.bash":      {Method: "session.bash", Class: ClassExec, NeedsIntent: true},

	// 状态变更。
	"session.set_model":           {Method: "session.set_model", Class: ClassState, NeedsIntent: true},
	"session.cycle_model":         {Method: "session.cycle_model", Class: ClassState, NeedsIntent: true},
	"session.set_thinking":        {Method: "session.set_thinking", Class: ClassState, NeedsIntent: true},
	"session.cycle_thinking":      {Method: "session.cycle_thinking", Class: ClassState, NeedsIntent: true},
	"session.set_queue_mode":      {Method: "session.set_queue_mode", Class: ClassState, NeedsIntent: true},
	"session.compact":             {Method: "session.compact", Class: ClassState, NeedsIntent: true},
	"session.set_auto_compaction": {Method: "session.set_auto_compaction", Class: ClassState, NeedsIntent: true},
	"session.set_auto_retry":      {Method: "session.set_auto_retry", Class: ClassState, NeedsIntent: true},
	"session.set_name":            {Method: "session.set_name", Class: ClassState, NeedsIntent: true},

	// 身份事务。
	"session.new":    {Method: "session.new", Class: ClassIdentity, NeedsIntent: true},
	"session.switch": {Method: "session.switch", Class: ClassIdentity, NeedsIntent: true},
	"session.fork":   {Method: "session.fork", Class: ClassIdentity, NeedsIntent: true},
	"session.clone":  {Method: "session.clone", Class: ClassIdentity, NeedsIntent: true},

	// 持久配置与删除。
	"config.models.write": {Method: "config.models.write", Class: ClassConfig, NeedsIntent: true},
	"sessions.delete":     {Method: "sessions.delete", Class: ClassDelete, NeedsIntent: true},

	// 终端。
	"terminal.open":   {Method: "terminal.open", Class: ClassTerminal, NeedsIntent: true},
	"terminal.input":  {Method: "terminal.input", Class: ClassInput},
	"terminal.resize": {Method: "terminal.resize", Class: ClassInput},
	"terminal.close":  {Method: "terminal.close", Class: ClassControl, Urgent: true},

	// 控制与对话。
	"session.abort":       {Method: "session.abort", Class: ClassControl, Urgent: true},
	"session.abort_retry": {Method: "session.abort_retry", Class: ClassControl, Urgent: true},
	"session.abort_bash":  {Method: "session.abort_bash", Class: ClassControl, Urgent: true},
	"session.ui_response": {Method: "session.ui_response", Class: ClassDialog, NeedsIntent: true, Urgent: true},

	// 有界产物。
	"session.export_html": {Method: "session.export_html", Class: ClassExport, NeedsIntent: true},
}

// SpecFor 返回命令的执行策略；未知方法第二个返回值为 false。
func SpecFor(method string) (Spec, bool) {
	spec, ok := specs[method]
	return spec, ok
}

// NeedsIntent 表示派发前必须可靠写 intent。
func NeedsIntent(method string) bool {
	spec, ok := specs[method]
	return ok && spec.NeedsIntent
}

// IsUrgent 表示控制命令，必须能插到普通写队列前面。
func IsUrgent(method string) bool {
	spec, ok := specs[method]
	return ok && spec.Urgent
}

// SpecCount 返回已声明策略的命令数，供跨包静态核对。
func SpecCount() int { return len(specs) }

// RecordsOutcome 表示该命令的结果是否需要持久回执。
// 只读、当前 worker 读取、磁盘读取与高频临时输入不写回执：
// 它们幂等，重复执行无害，写盘只会造成无谓 churn
// （前端轮询状态时尤其明显）。
func RecordsOutcome(method string) bool {
	spec, ok := specs[method]
	if !ok {
		return false
	}
	switch spec.Class {
	case ClassRead, ClassWorkerRead, ClassDiskRead, ClassInput:
		return false
	}
	return true
}
