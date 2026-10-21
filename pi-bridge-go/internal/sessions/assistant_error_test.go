package sessions

import (
	"strings"
	"testing"
)

// TestAssistantError按类别投影：只把**已知**的失败类别翻成固定文案。
//
// 两条判据上的坚持，都被反例钉住：
//
//  1. 402 还可能是套餐不含该模型、账户冻结等原因，只凭状态码就告诉用户
//     「余额不足」会把排查引向错误方向；必须**同时**出现 insufficient balance。
//  2. 判定不能靠裸子串找数字：错误体 JSON 里常带 "code":"upstream_429"
//     这类字样，裸匹配会把 400 误判成限流。
//
// 用例文本取自真实语料（会话 JSONL 里的 errorMessage），不是凭空构造。
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

		// 限流：三种信封都要认（行首裸响应、CPA 代理信封、OpenAI 兼容信封）。
		{
			name: "429 行首裸响应", stopReason: "error",
			message: `429 {"error":{"message":"Rate limit reached, please try again later.","type":"rate_limited"}}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "429 CPA 代理信封", stopReason: "error",
			message: `CPA-Responses API error (429): {"code":"upstream_429","message":"upstream_429","type":"upstream_error"}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "429 OpenAI 兼容信封", stopReason: "error",
			message: `OpenAI API error (429): {"code":"rate_limit_reached","message":"rate_limit_reached","type":"upstream_error"}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},
		{
			name: "429 免费额度用尽", stopReason: "error",
			message: `CPA-Responses API error (429): {"code":"FreeUsageLimitError","message":"FreeUsageLimitError","type":"upstream_error"}`,
			want:    "模型请求失败：供应商限流（HTTP 429），请稍后重试。",
		},

		// 认证
		{
			name: "401 认证失败", stopReason: "error",
			message: `401 "Invalid API key"`,
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},
		{
			name: "403 认证失败", stopReason: "error",
			message: "403 forbidden",
			want:    "模型请求失败：供应商拒绝认证，请检查模型凭据。",
		},

		// 凭据不可用/冷却：与「限流」不同，这不是用量打满，需要人检查凭据。
		{
			name: "凭据不可用", stopReason: "error",
			message: `CPA-Responses API error (503): {"message":"auth_unavailable: no auth available (providers=step, model=step-5-preview)","type":"server_error"}`,
			want:    "模型请求失败：该模型当前没有可用凭据（未登录或正在冷却），请稍后重试或检查该供应商的凭据。",
		},
		{
			name: "凭据冷却中", stopReason: "error",
			message: `429 {"error":{"code":"model_cooldown","message":"All credentials for model step-5-preview are cooling down"}}`,
			want:    "模型请求失败：该模型当前没有可用凭据（未登录或正在冷却），请稍后重试或检查该供应商的凭据。",
		},

		// 模型未找到：配置问题，稳定复现，必须与「偶发失败」区分开。
		{
			name: "模型未找到 400", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"model_not_found","message":"unknown provider for model deepseek-flash","param":"model"}`,
			want:    "模型请求失败：供应商不认识该模型，请检查模型配置里该模型与供应商的对应关系。",
		},
		{
			name: "模型未找到 502", stopReason: "error",
			message: `CPA-Responses API error (502): {"message":"unknown provider for model gpt-6.1-sol","type":"server_error"}`,
			want:    "模型请求失败：供应商不认识该模型，请检查模型配置里该模型与供应商的对应关系。",
		},

		// 内容策略拦截
		{
			name: "内容策略拦截", stopReason: "error",
			message: `cyber_policy: This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request.`,
			want:    "模型请求失败：请求被上游内容策略拦截，可改写措辞后重试。",
		},

		// 思考内容回传被拒：Pi 与上游的协议问题，能自愈，但要提示换模型。
		{
			name: "思考内容回传被拒", stopReason: "error",
			message: `OpenAI API error (400): {"message":"The ` + "`reasoning_text`" + ` in the thinking mode must be passed back to the API.","type":"invalid_request_error"}`,
			want:    "模型请求失败：思考内容回传被上游拒绝，可重试；若持续出现请更换模型。",
		},

		// 请求体被上游拒（400 系列）
		{
			name: "上游拒绝请求内容 invalid_parameter", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"invalid_parameter","message":"invalid_parameter","param":"","type":"upstream_error"}`,
			want:    "模型请求失败：上游拒绝了请求内容，可重试；若持续出现请检查模型配置。",
		},
		{
			name: "上游拒绝请求内容 validation errors", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"upstream_request_rejected","message":"167 validation errors:\n  {'type': 'string_type'}"}`,
			want:    "模型请求失败：上游拒绝了请求内容，可重试；若持续出现请检查模型配置。",
		},
		{
			name: "上游 400 bad_response_status_code", stopReason: "error",
			message: `400 {"type":"error","error":{"type":"bad_response_status_code","message":"bad_response_status_code"}}`,
			want:    "模型请求失败：上游拒绝了请求内容，可重试；若持续出现请检查模型配置。",
		},

		// 上游响应流中断：能自愈，措辞应说「可重试」。
		{
			name: "上游流中断 Anthropic", stopReason: "error",
			message: "Anthropic stream ended without a stop reason",
			want:    "模型请求失败：上游响应流中断，可重试。",
		},
		{
			name: "上游流中断 empty_stream", stopReason: "error",
			message: `CPA-Responses API error (500): {"message":"empty_stream: upstream stream closed before first payload","type":"server_error"}`,
			want:    "模型请求失败：上游响应流中断，可重试。",
		},
		{
			name: "上游流中断 terminal event", stopReason: "error",
			message: "upstream stream closed before a terminal event (last event: response.output_item.done)",
			want:    "模型请求失败：上游响应流中断，可重试。",
		},
		{
			name: "上游流中断 http2 stream error", stopReason: "error",
			message: "Error Code internal_server_error: stream error: stream ID 17; INTERNAL_ERROR; received from peer",
			want:    "模型请求失败：上游响应流中断，可重试。",
		},

		// 上游服务端故障
		{
			name: "上游 500", stopReason: "error",
			message: `OpenAI API error (500): {"code":"do_request_failed","message":"do_request_failed","type":"upstream_error"}`,
			want:    "模型请求失败：供应商服务端故障（HTTP 5xx），请稍后重试。",
		},
		{
			name: "上游 503 服务繁忙", stopReason: "error",
			message: `OpenAI API error (503): {"message":"Service is too busy. We advise users to temporarily switch to alternative LLM API service providers.","type":"api_error"}`,
			want:    "模型请求失败：供应商服务端故障（HTTP 5xx），请稍后重试。",
		},

		// 网络中断：实测这类多发生在本地代理重启时，措辞不得把用户引向「供应商」。
		{
			name: "网络连接错误", stopReason: "error",
			message: "Connection error.",
			want:    "模型请求失败：请求中断（连接错误或超时）。本地代理重启或网络波动时会这样，可重试。",
		},
		{
			name: "网络被终止", stopReason: "error",
			message: "terminated",
			want:    "模型请求失败：请求中断（连接错误或超时）。本地代理重启或网络波动时会这样，可重试。",
		},
		{
			name: "网络超时", stopReason: "error",
			message: "Request timed out.",
			want:    "模型请求失败：请求中断（连接错误或超时）。本地代理重启或网络波动时会这样，可重试。",
		},

		// 上游其它：信息量低，但仍比兜底句有用。
		{
			name: "上游其它 upstream_error", stopReason: "error",
			message: "upstream_error: upstream_error",
			want:    "模型请求失败：上游返回错误，可重试。",
		},

		// 反例：不得靠裸子串找状态码。
		{
			name: "括号式 400 正文含 429 字样不得误判限流", stopReason: "error",
			message: `CPA-Responses API error (400): {"code":"upstream_429_like_code","message":"invalid_parameter"}`,
			want:    "模型请求失败：上游拒绝了请求内容，可重试；若持续出现请检查模型配置。",
		},

		// 确实没有可归类信息的，仍走兜底。
		{
			name: "错误消息缺失时给通用提示", stopReason: "error",
			message: "",
			want:    generic,
		},
		{
			name: "无法识别的原因走通用", stopReason: "error",
			message: `{"type":"error"}`,
			want:    generic,
		},
		{
			// `This operation was aborted` 配 stopReason=error：实测 22 条里
			// 内容全空、usage 全零、距上一条消息 0.0–0.3 秒——是请求刚发出
			// 就被取消（用户中止/切会话）。
			//
			// 这条曾经是「暂不归类」，落进 generic。但 generic 说的是
			// 「请检查供应商连接、账户状态与模型配置」——恰恰是把用户自己
			// 取消的动作说成了供应链故障，正是当初想避免的那件事。
			// 现在归到「已中止」：判据见 isManualAbort。
			name: "中止语义明确，归为已中止", stopReason: "error",
			message: "This operation was aborted",
			want:    "模型请求已中止。",
		},
		{
			name: "结束原因不是 error 时不投影", stopReason: "endTurn",
			message: "402 insufficient balance",
			want:    "",
		},
		{
			// aborted 不是 error：手动中止不投影，前端 turn.Error 为空不渲染。
			name: "结束原因是 aborted 时不投影", stopReason: "aborted",
			message: "Request aborted",
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
	leaks := []string{"sk-secret-value", "rid-123", "insufficient", "upstream_error", "cooling down"}
	msgs := []string{
		`402 insufficient balance sk-secret-value request_id=rid-123`,
		`CPA-Responses API error (503): {"message":"auth_unavailable sk-secret-value request_id=rid-123"}`,
		`CPA-Responses API error (429): {"code":"upstream_429 sk-secret-value request_id=rid-123"}`,
		`OpenAI API error (500): {"code":"do_request_failed sk-secret-value request_id=rid-123"}`,
	}
	for _, msg := range msgs {
		got := assistantError("error", msg)
		for _, leak := range leaks {
			if strings.Contains(got, leak) {
				t.Fatalf("摘要泄漏了原文内容 %q: %s", leak, got)
			}
		}
	}
}

// 手动中止不能报成「请检查供应商连接、账户状态与模型配置」。
//
// 真实语料（本机 5 个会话、22 条记录）里有一类固定形态：用户点了中止，
// Pi 把这次空响应记成 `stopReason: "error"` 而不是 `"aborted"`，
// `errorMessage` 是 `This operation was aborted`。桥的规则表里没有它，
// 就落进 default 那条兜底文案——把用户自己掐断的动作说成供应链故障，
// 排查方向完全错误。
//
// 判定必须同时看两件事：文案是中止类，且这条消息**没有正文**。
// 只看文案不够——真实的中止与被上游掐断在文本上可能同形。
func Test手动中止不报供应商故障(t *testing.T) {
	aborted := assistantError("error", "This operation was aborted")
	if aborted == "" {
		t.Fatal("中止应给出可读提示，而不是当成正常结束静默")
	}
	if strings.Contains(aborted, "供应商连接") || strings.Contains(aborted, "账户状态") {
		t.Fatalf("用户自己掐断的回合不该说成供应链故障：%q", aborted)
	}
	for _, want := range []string{"已中止", "已停止", "已取消"} {
		if strings.Contains(aborted, want) {
			return
		}
	}
	t.Fatalf("提示应说明是用户中止：%q", aborted)
}

// 中止类的几种真实变体都要被识别（取自本机语料）。
func Test中止文案的变体(t *testing.T) {
	for _, message := range []string{
		"This operation was aborted",
		"Request aborted",
		"The operation was aborted.",
		"Aborted",
	} {
		got := assistantError("error", message)
		if strings.Contains(got, "供应商连接") {
			t.Fatalf("%q 被误判为供应链故障: %q", message, got)
		}
		if got == "" {
			t.Fatalf("%q 应给出中止提示", message)
		}
	}
}

// 中止判定不能吞掉真正的中断类错误：超时/连接错误仍要说「可重试」，
// 且仍要提示本地代理与网络这条真实原因。
func Test超时不被当中止(t *testing.T) {
	for _, message := range []string{"Request timed out.", "Connection error.", "terminated"} {
		got := assistantError("error", message)
		if strings.Contains(got, "中止") {
			t.Fatalf("%q 不是中止，却给了中止提示: %q", message, got)
		}
	}
}

// 端到端：真实形态的中止记录经投影后，产物是「已中止」而不是通用故障文案。
//
// projectMessage 收到的是 message 对象（store.go 的调用方已经剥掉外层
// entry），所以这里的 raw 只含 role/content/stopReason/errorMessage。
// 内容为空正是这类记录的特征——用户点了中止，模型什么都没产出。
func Test中止记录经投影给出已中止(t *testing.T) {
	raw := []byte(`{"role":"assistant","content":[],"api":"openai-responses","provider":"CPA-Responses","model":"deepseek-v4.1-flash","usage":{"input":0,"output":0},"stopReason":"error","errorMessage":"This operation was aborted","timestamp":1792161449047}`)
	pm := projectMessage(raw)
	if pm.role != "assistant" {
		t.Fatalf("role = %q，期望 assistant", pm.role)
	}
	if pm.errText != "模型请求已中止。" {
		t.Fatalf("中止记录投影 = %q，期望「模型请求已中止。」", pm.errText)
	}
}
