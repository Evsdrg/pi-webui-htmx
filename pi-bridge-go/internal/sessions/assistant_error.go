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
//	括号内    `OpenAI API error (429): {…}`  —— 代理包装后的信封
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

// isManualAbort 判断这条错误是不是「用户自己掐断了回合」。
//
// 为什么需要单独判：Pi 大多数时候把中止记成 stopReason="aborted"（此时
// 根本不会走到这里），但实测有一类记录是 stopReason="error" 且
// errorMessage 为 "This operation was aborted"——那是空响应被中止时的形态。
// 不识别它，用户点一下停止就会看到「请检查供应商连接、账户状态与模型配置」。
//
// 判据只认「abort」这个词本身，且要求它是完整词：用 contains("abort")
// 会把 "upstream_aborted_connection" 之外的东西也吞进来，但更重要的是
// **不能**把连接中断类（timed out / connection error / terminated）误判成
// 中止——那条要保留「可重试 + 本地代理/网络」的提示，两者给用户的动作不同。
//
// 因此这里刻意不把 "interrupted" 收进来：它同时出现在上游流中断的语料里
// （见下面 stream 那条规则），归它更准确。
func isManualAbort(body string) bool {
	if !strings.Contains(body, "abort") {
		return false
	}
	// 排除「被上游/连接层掐断」这类同样含 abort 字样的描述。
	if hasAny(body, "upstream", "connection", "socket", "tcp", "timeout", "timed out") {
		return false
	}
	return true
}

// hasAny 判断文本里是否出现任一标记。标记一律用小写，调用方先 ToLower。
func hasAny(body string, marks ...string) bool {
	for _, m := range marks {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// assistantError 只投影已知失败类别，不把上游原文或请求标识送给浏览器。
// stopReason 与 errorMessage 由调用方从消息里一次解出（见 projectMessage）；
// stopReason 不是 error 时不投影。
//
// 规则按「最具体优先」排列，先命中先返回——顺序本身是语义：
//
//   - 冷却要排在限流之前：`429 …cooling down` 是「没有可用凭据」，不是用量打满，
//     给用户的动作不同（检查凭据 vs 等一会儿）。
//   - 流中断要排在 5xx 之前：`(500) empty_stream` 的根因是流断了，不是服务端故障。
//   - 凭据不可用要排在 5xx 之前：`(503) auth_unavailable` 同理。
//
// 措辞上区分「能自愈」（说可重试）与「需人检查」（说检查什么）：偶发的中断、
// 限流、超时都属前者；模型未找到、凭据不可用属后者，稳定复现。
func assistantError(stopReason, errorMessage string) string {
	if stopReason != "error" {
		return ""
	}
	body := strings.ToLower(errorMessage)
	status := statusOf(body)
	switch {
	case isManualAbort(body):
		// 用户点了中止。**必须**排在最前：Pi 有时把这次空响应记成
		// stopReason="error" 而不是 "aborted"（本机语料 22 条，
		// errorMessage 是 "This operation was aborted"），落进下面的规则
		// 会被说成供应链故障，把排查方向彻底带偏。
		//
		// 文案不写「失败」：这不是故障，是用户自己停的。也不提「重试」，
		// 因为用户要的往往就是停下来。
		return "模型请求已中止。"

	case status == 402 && strings.Contains(body, "insufficient balance"):
		// 402 还可能是套餐不含该模型、账户冻结等原因，只凭状态码就告诉
		// 用户「余额不足」会把排查引向错误方向；必须同时出现该短语。
		return "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。"

	case hasAny(body, "auth_unavailable", "no auth available", "cooling down", "model_cooldown"):
		return "模型请求失败：该模型当前没有可用凭据（未登录或正在冷却），请稍后重试或检查该供应商的凭据。"

	case hasAny(body, "unknown provider", "model_not_found"):
		return "模型请求失败：供应商不认识该模型，请检查模型配置里该模型与供应商的对应关系。"

	case hasAny(body, "cyber_policy", "content_policy", "content policy", "flagged for"):
		return "模型请求失败：请求被上游内容策略拦截，可改写措辞后重试。"

	case status == 401 || status == 403:
		return "模型请求失败：供应商拒绝认证，请检查模型凭据。"

	case status == 429 || hasAny(body, "rate_limit", "rate limit", "freeusagelimit", "too many requests"):
		return "模型请求失败：供应商限流（HTTP 429），请稍后重试。"

	case strings.Contains(body, "reasoning_text"):
		// Pi 把思考内容回传给上游时的协议问题，实测集中在某个模型上，
		// 换模型即可绕开，所以措辞给「重试或换模型」而不是查配置。
		return "模型请求失败：思考内容回传被上游拒绝，可重试；若持续出现请更换模型。"

	case status == 400 || hasAny(body, "invalid_parameter", "invalid_request", "validation error", "bad_response_status_code"):
		return "模型请求失败：上游拒绝了请求内容，可重试；若持续出现请检查模型配置。"

	case hasAny(body, "stream ended without", "stream closed before", "empty_stream",
		"upstream stream", "terminal event", "terminal response", "interrupted", "stream error"):
		return "模型请求失败：上游响应流中断，可重试。"

	case status >= 500:
		return "模型请求失败：供应商服务端故障（HTTP 5xx），请稍后重试。"

	case hasAny(body, "connection error", "connection timed out", "request timed out",
		"timed out", "read tcp", "terminated") || status == 408:
		// 实测这类多发生在本地代理重启时（掐断进行中的流），措辞不得把用户
		// 引向「供应商连接」——那是排查方向里最容易被误导的一条。
		return "模型请求失败：请求中断（连接错误或超时）。本地代理重启或网络波动时会这样，可重试。"

	case strings.Contains(body, "upstream"):
		return "模型请求失败：上游返回错误，可重试。"

	default:
		return "模型请求失败，请检查供应商连接、账户状态与模型配置。"
	}
}
