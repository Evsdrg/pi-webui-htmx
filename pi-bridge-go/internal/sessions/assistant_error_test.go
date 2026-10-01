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
		name string
		raw  string
		want string
	}{
		{
			name: "402 且明说余额不足",
			raw:  `{"stopReason":"error","errorMessage":"402 {\"error\":{\"message\":\"insufficient balance (1008)\"}}"}`,
			want: "模型请求失败：供应商余额不足（HTTP 402），请检查额度或稍后重试。",
		},
		{
			name: "402 但不是余额不足",
			raw:  `{"stopReason":"error","errorMessage":"402 payment required: model not included in current plan"}`,
			want: generic,
		},
		{
			name: "429 限流",
			raw:  `{"stopReason":"error","errorMessage":"429 too many requests"}`,
			want: "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "401 认证失败",
			raw:  `{"stopReason":"error","errorMessage":"401 invalid api key"}`,
			want: "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "403 认证失败",
			raw:  `{"stopReason":"error","errorMessage":"403 forbidden"}`,
			want: "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "500 通用",
			raw:  `{"stopReason":"error","errorMessage":"500 upstream error"}`,
			want: generic,
		},
		{
			name: "结束原因不是 error 时不投影",
			raw:  `{"stopReason":"endTurn","errorMessage":"402 insufficient balance"}`,
			want: "",
		},
		{
			name: "不是 JSON 时不投影",
			raw:  `not json`,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := assistantError([]byte(c.raw)); got != c.want {
				t.Fatalf("投影结果不符\n得到: %q\n期望: %q", got, c.want)
			}
		})
	}
}

// TestAssistantError不回显原文：摘要里不得出现供应商原文、密钥或请求标识。
func TestAssistantError不回显原文(t *testing.T) {
	raw := []byte(`{"stopReason":"error","errorMessage":"402 insufficient balance sk-secret-value request_id=rid-123"}`)
	got := assistantError(raw)
	for _, leak := range []string{"sk-secret-value", "rid-123", "insufficient"} {
		if strings.Contains(got, leak) {
			t.Fatalf("摘要泄漏了原文内容 %q: %s", leak, got)
		}
	}
}
