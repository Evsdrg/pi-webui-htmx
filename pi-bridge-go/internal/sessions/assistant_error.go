package sessions

import (
	"regexp"
	"strconv"
	"strings"
)

// statusPattern 从错误文本里按**位置形态**提取 HTTP 状态码。
//
// 为什么不用 strings.Contains(body, "429")：错误体的 JSON 里常带
// "code":"upstream_429" 这类字符串（实测），裸子串匹配会把 400 误判成
// 限流，把排查引向错误方向。状态码在实测里只有三种落位，只认这三种：
//
//	行首      `429 {"error":…}`              —— 上游直出的裸响应
//	括号内    `OpenAI API error (429): {…}`  —— 代理包装后的信封（此前漏判）
//	HTTP 前缀 `HTTP 429 Too Many Requests`
var statusPattern = regexp.MustCompile(`(?i)(?:^(\d{3})(?:\s|$))|(?:\((\d{3})\))|(?:HTTP\s*(\d{3}))`)

// statusOf 返回错误文本里识别到的 HTTP 状态码；识别不到返回 0。
func statusOf(body string) int {
	m := statusPattern.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	for _, g := range m[1:] {
		if g == "" {
			continue
		}
		if n, err := strconv.Atoi(g); err == nil {
			return n
		}
	}
	return 0
}

// assistantError 只投影已知失败类别，不把上游原文或请求标识送给浏览器。
// stopReason 与 errorMessage 由调用方从消息里一次解出（见 projectMessage）；
// stopReason 不是 error 时不投影。
//
// 实测：本地 CPA 代理把上游错误包装成 `CPA-Responses API error (429): {…}`，
// 状态码不在行首。早先只认行首前缀，于是这类错误全落到通用提示——「供应商
// 限流」的专门文案写了却打不开。因此改为按形态提取状态码。
func assistantError(stopReason, errorMessage string) string {
	if stopReason != "error" {
		return ""
	}
	body := strings.ToLower(errorMessage)
	status := statusOf(body)
	switch {
	case status == 402 && strings.Contains(body, "insufficient balance"):
		// 402 还可能是套餐不含该模型、账户冻结等原因，只凭状态码就告诉
		// 用户「余额不足」会把排查引向错误方向；必须同时出现该短语。
		return "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。"
	case status == 429:
		return "模型请求失败：供应商限流（HTTP 429），请稍后重试。"
	case status == 401 || status == 403:
		return "模型请求失败：供应商拒绝认证，请检查模型凭据。"
	default:
		return "模型请求失败，请检查供应商连接、账户状态与模型配置。"
	}
}
