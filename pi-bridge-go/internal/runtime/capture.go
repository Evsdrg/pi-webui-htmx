package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// CapturedTool 是捕获扩展落盘的一个工具定义（参数为 JSON Schema 原文）。
type CapturedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// CapturedRequest 是桥内捕获扩展落盘的「实际下发给模型」的系统提示词与工具定义。
// SessionID 用于校验文件确实属于当前会话（同一结果目录可能残留其它会话的文件）。
type CapturedRequest struct {
	SessionID    string         `json:"sessionId"`
	SystemPrompt string         `json:"systemPrompt"`
	Tools        []CapturedTool `json:"tools"`
	At           int64          `json:"at"`
}

// CaptureResultDir 返回本 worker 的运行时捕获结果目录（未启用时为空）。
// 运行时代码用它在正确位置找捕获文件；测试用它在同一位置注入夹具。
func (w *Worker) CaptureResultDir() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.captureResultDir
}

// CapturedRequest 读取本 worker 对应会话的运行时捕获结果。
//
// 与 session.navigate 的结果文件不同，这里不做「先删旧文件再等新文件」的握手：
// 捕获文件是幂等快照，读到旧值也只反映上一次请求的实际载荷，不需要报错。
// 第二个返回值为 false 表示尚未产生任何请求（或未启用 / 会话不匹配），
// 调用方应回退到 export_html 基线。
func (w *Worker) CapturedRequest(sessionID string) (CapturedRequest, bool) {
	w.mu.Lock()
	dir := w.captureResultDir
	w.mu.Unlock()
	// sessionID 直接来自 Pi，理论上已是 UUID 形态；仍挡一次路径穿越。
	if dir == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return CapturedRequest{}, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		return CapturedRequest{}, false
	}
	var out CapturedRequest
	if json.Unmarshal(raw, &out) != nil || out.SessionID != sessionID || out.SystemPrompt == "" {
		return CapturedRequest{}, false
	}
	return out, true
}
