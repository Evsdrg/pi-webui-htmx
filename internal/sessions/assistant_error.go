package sessions

import (
	"encoding/json"
	"strings"
)

// assistantError 只投影已知失败类别，不把上游原文或请求标识送给浏览器。
func assistantError(raw json.RawMessage) string {
	var message struct {
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(raw, &message) != nil || message.StopReason != "error" {
		return ""
	}
	body := strings.ToLower(message.ErrorMessage)
	switch {
	case strings.HasPrefix(body, "402 ") && strings.Contains(body, "insufficient balance"):
		return "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。"
	case strings.HasPrefix(body, "429 "):
		return "模型请求失败：供应商限流（HTTP 429），请稍后重试。"
	case strings.HasPrefix(body, "401 "), strings.HasPrefix(body, "403 "):
		return "模型请求失败：供应商拒绝认证，请检查模型凭据。"
	default:
		return "模型请求失败，请检查供应商连接、账户状态与模型配置。"
	}
}
