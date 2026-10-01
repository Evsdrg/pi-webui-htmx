package sessions

import (
	"bytes"
	"encoding/json"

	"pi-bridge-go/internal/protocol"
)

// recordRef 是一条记录里建索引所需的全部信息。
type recordRef struct {
	id     string
	parent string
	// parentNull 表示 parentId 显式为 null，即没有父亲。
	parentNull bool
	// isUser 表示这是一条 user 消息，是轮边界对齐要找的锚点。
	isUser bool
	// isModelChange 让调用方知道要不要把 lastModelID 更新为自己。
	isModelChange bool
}

// parseRecordRef 解析一条记录，只取建索引所需的字段。
//
// 正向全扫（scanFile）与反向窗口扫（scanTail）共用它，避免两处解析规则
// 漂移——尤其是快路径与慢路径的分界、以及「快路径没看正文要补一次结构
// 校验」这条（B13），漏在任一侧都会静默接受损坏的记录。
//
// 这里只判断「这条记录本身是否合法」。父链是否连通、ID 是否重复取决于
// 扫描方向与已见集合，由调用方各自判断。
func parseRecordRef(b []byte) (recordRef, error) {
	head, fast := parseEntryHead(b)
	if fast {
		if head.Type == "" || head.Type == "session" || !ValidID(head.ID) || !head.HasParent {
			return recordRef{}, protocol.E("invalid_history", "完整的历史记录格式错误")
		}
		// 快路径没看正文，必须补一次结构校验，否则正文损坏的记录会被静默接受（B13）。
		if !balancedJSON(b) {
			return recordRef{}, protocol.E("invalid_history", "完整的历史记录格式错误")
		}
		parent := head.Parent // parentId 为 null 时天然是空串
		if !head.ParentNull && !ValidID(parent) {
			return recordRef{}, protocol.E("invalid_history", "父条目 ID 无效")
		}
		return recordRef{
			id: head.ID, parent: parent, parentNull: head.ParentNull,
			isUser: head.IsUser, isModelChange: head.Type == "model_change",
		}, nil
	}
	// 慢路径：完整解析。这里刻意自己校验并返回错误，
	// 而不是把结果塞进 entryHead——原来用 json.RawMessage
	// 承载 parentId，能区分「解析失败」和「解出来是空」；
	// 硬塞进 entryHead 会把「parentId 是数字」这类错误
	// 从「父条目 ID 无效」悄悄变成「父链断裂」。
	var item struct {
		Type   string          `json:"type"`
		ID     string          `json:"id"`
		Parent json.RawMessage `json:"parentId"`
		// Role 随同一次解析取出：message 的其余成员（可能是上百 KB
		// 的正文）会被跳过而不复制。
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &item) != nil || item.Type == "" || item.Type == "session" || !ValidID(item.ID) || len(item.Parent) == 0 {
		return recordRef{}, protocol.E("invalid_history", "完整的历史记录格式错误")
	}
	parent := ""
	parentNull := true
	if !bytes.Equal(item.Parent, []byte("null")) {
		if json.Unmarshal(item.Parent, &parent) != nil || !ValidID(parent) {
			return recordRef{}, protocol.E("invalid_history", "父条目 ID 无效")
		}
		parentNull = false
	}
	return recordRef{
		id: item.ID, parent: parent, parentNull: parentNull,
		isUser: item.Type == "message" && item.Message.Role == "user", isModelChange: item.Type == "model_change",
	}, nil
}
