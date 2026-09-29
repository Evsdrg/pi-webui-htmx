package runtime

import "encoding/json"

// 命令回执的具名类型。
//
// 与 transport/responses.go 同一条规则：**固定形状**的回执用具名类型，
// 键名（线上格式）写进 json 标签，写错键不再是编译器看不见的错误。
//
// 这里不覆盖两类 map，它们保持 map[string]any 是有意的：
//   - 用户文档透传（models.json / settings.json / trust.json 的内容）：
//     形状由用户决定，类型化等于假装它有固定模式。
//   - 诊断快照（/healthz 的 stats 之类）：采集面本身是开放的，
//     类型化只增加维护成本。

// ModelRef 是当前生效的模型。
type ModelRef struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider"`
}

// SetModelReply 是 session.set_model 的回执。
type SetModelReply struct {
	ModelRef
}

// CycleModelReply 是 session.cycle_model 的回执。
// 切模型会连带重置思考等级，所以这里要把生效后的等级一起返回。
type CycleModelReply struct {
	ModelRef
	ThinkingLevel string `json:"thinkingLevel"`
}

// CompactReply 是 session.compact 的回执。
// TokensBefore / EstimatedTokensAfter 是 Pi 的估算，不是精确计数。
type CompactReply struct {
	Summary              string `json:"summary"`
	FirstKeptEntryID     string `json:"firstKeptEntryId"`
	TokensBefore         int    `json:"tokensBefore"`
	EstimatedTokensAfter int    `json:"estimatedTokensAfter"`
}

// EntriesReply 是 session.entries 的回执：原始条目与当时的叶子。
type EntriesReply struct {
	Entries []json.RawMessage `json:"entries"`
	LeafID  string            `json:"leafId"`
}

// ForkReply 是 session.fork 的回执。
//
// Persisted 表示磁盘索引是否已经能找到新会话。Pi 在分支还没有 assistant
// 记录时延迟写盘，因此 persisted:false 的新 ID **只在当前 worker 中存在**；
// 关闭 worker 前没有首条 assistant 回复就无法恢复。调用方不得按这个 ID
// 直接请求磁盘历史（api/v1/protocol.md「分支与导出」一节）。
type ForkReply struct {
	SessionID string `json:"sessionId"`
	// Text 是原用户消息，供新分支的草稿继续编辑。
	Text      string `json:"text"`
	Persisted bool   `json:"persisted"`
}
