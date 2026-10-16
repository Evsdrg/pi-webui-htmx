package sessions

import (
	"strings"
	"testing"
)

// TestAssistantError按类别投影：只把**已知**的失败类别翻成固定文案。
//
// 重点是「402 但不是余额不足」这一条：402 还可能是套餐不含该模型、
// 账户冻结等原因，只凭状态码就告诉用户「余额不足」会把排查引向错误方向。
// 判据是「状态码前缀 **且** 出现 insufficient balance」，两个条件都要成立。
func TestAssistantError按类别投影(t *testing.T) {
	const (
		generic = "模型请求失败，请检查供应商连接、账户状态与模型配置。"
	)
	cases := []struct {
		name       string
		stopReason string
		message    string
		want       string
	}{
		{
			name: "402 且明说余额不足", stopReason: "error",
			message: `402 {"error":{"message":"insufficient balance (1008)"}}`,
			want:    "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。",
		},
		{
			name: "402 但不是余额不足", stopReason: "error",
			message: "402 payment required: model not included in current plan",
			want:    generic,
		},
		{
			name: "429 限流", stopReason: "error",
			message: "429 too many requests",
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		// 代理包装过的错误：状态码不在行首，而是写在括号里。实测两种信封
		// （CPA 本地代理与 OpenAI 兼容上游）都是这个形态，此前一律漏判成
		// 通用提示——「供应商限流」的专门文案写了却打不开。
		{
			name: "CPA 代理包装的 429", stopReason: "error",
			message: `CPA-Responses API error (429): {"code":"upstream_429","message":"upstream_429","type":"upstream_error"}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "OpenAI 代理包装的 429", stopReason: "error",
			message: `OpenAI API error (429): {"code":"rate_limit_reached","message":"rate_limit_reached","type":"upstream_error"}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "括号式 402 且余额不足", stopReason: "error",
			message: `OpenAI API error (402): {"message":"insufficient balance (1008)","type":"upstream_error"}`,
			want:    "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。",
		},
		{
			name: "括号式 401", stopReason: "error",
			message: `OpenAI API error (401): {"message":"Invalid API key"}`,
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "括号式 403", stopReason: "error",
			message: `CPA-Responses API error (403): {"message":"forbidden"}`,
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "HTTP 形态的状态码", stopReason: "error",
			message: "HTTP 429 Too Many Requests",
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		// 反例：不得靠裸子串找状态码。错误体里常有 "upstream_429" 这类
		// code 字符串，裸匹配会把 400 误判成限流、把用户引向错误方向。
		{
			name: "括号式 400 正文含 429 字样不得误判限流", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"upstream_429_like_code","message":"invalid_parameter"}`,
			want:    generic,
		},
		// 反例：通用错误体里出现 `(400)` 这类，仍应走通用提示（400 无专门文案）。
		{
			name: "括号式 400 走通用", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"invalid_parameter","message":"invalid_parameter"}`,
			want:    generic,
		},
		{
			name: "括号式 500 走通用", stopReason: "error",
			message: `OpenAI API error (500): {"code":"do_request_failed","type":"upstream_error"}`,
			want:    generic,
		},
		{
			name: "401 认证失败", stopReason: "error",
			message: "401 invalid api key",
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "403 认证失败", stopReason: "error",
			message: "403 forbidden",
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "500 通用", stopReason: "error",
			message: "500 upstream error",
			want:    generic,
		},
		{
			// 错误消息缺失（null/类型异常在投影层解成空串）也要给通用提示。
			name: "错误消息缺失时给通用提示", stopReason: "error",
			message: "",
			want:    generic,
		},
		{
			name: "结束原因不是 error 时不投影", stopReason: "endTurn",
			message: "402 insufficient balance",
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := assistantError(c.stopReason, c.message); got != c.want {
				t.Fatalf("投影结果不符\n得到: %q\n期望: %q", got, c.want)
			}
		})
	}
}

// TestAssistantError不回显原文：摘要里不得出现供应商原文、密钥或请求标识。
func TestAssistantError不回显原文(t *testing.T) {
	got := assistantError("error", `402 insufficient balance sk-secret-value request_id=rid-123`)
	for _, leak := range []string{"sk-secret-value", "rid-123", "insufficient"} {
		if strings.Contains(got, leak) {
			t.Fatalf("摘要泄漏了原文内容 %q: %s", leak, got)
		}
	}
}
