// ProjectEntries 解码收敛的差分测试。
//
// 背景：投影曾对同一条 message 解码四到五次（角色+文本、惰性块、错误、
// 用量、工具元数据各一次）；收敛为「一次外层解码 + 少量单字段解码」时
// 最大的风险是共用结构：原型用一份严格结构一次性解码整条消息，某个
// 字段类型异常会让**整条消息**清零——角色、正文、工具名全部消失。
//
// 下面的语料把畸形输入的行为逐条钉住。原则：
//   - 单字段异常只降级它自己（usage 为 nil、工具名为空、该块跳过），
//     正文与其它字段必须保留；
//   - 消息整体不可解析（外层 JSON 坏）才允许整条降级；
//   - 正常形状的行为与旧实现逐字节一致（由既有测试继续覆盖）。
package sessions

import (
	"encoding/json"
	"strings"
	"testing"
)

func Test投影解码收敛的差分语料(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, e Entry)
	}{
		{
			name: "纯文本用户消息",
			raw:  `{"type":"message","id":"m1","message":{"role":"user","content":"问题"}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindUser || e.Text != "问题" {
					t.Fatalf("投影异常: %+v", e)
				}
			},
		},
		{
			name: "助手多块：文本以空格连接，思考生成占位符",
			raw:  `{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"thinking","thinking":"想"},{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindAssistant || e.Text != "a b" {
					t.Fatalf("投影异常: %+v", e)
				}
				if len(e.Lazy) != 1 || e.Lazy[0] != (LazyBlock{Kind: "thinking", BlockIndex: 0}) {
					t.Fatalf("思考占位符异常: %+v", e.Lazy)
				}
			},
		},
		{
			name: "助手错误与用量",
			raw:  `{"type":"message","id":"m3","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"402 {\"error\":{\"message\":\"insufficient balance\"}}","usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"cost":{"total":0.5}}}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindAssistant || !strings.Contains(e.Error, "余额") {
					t.Fatalf("错误投影异常: %+v", e)
				}
				if e.Usage == nil || e.Usage.Input != 1 || e.Usage.Output != 2 || e.Usage.CacheRead != 3 || e.Usage.CacheWrite != 4 || e.Usage.Cost != 0.5 {
					t.Fatalf("用量投影异常: %+v", e.Usage)
				}
			},
		},
		{
			name: "工具结果：名称、失败标记、正文",
			raw:  `{"type":"message","id":"m4","message":{"role":"toolResult","toolName":"bash","isError":true,"content":[{"type":"text","text":"输出"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindTool || e.ToolName != "bash" || !e.Failed || e.Text != "输出" {
					t.Fatalf("工具投影异常: %+v", e)
				}
			},
		},
		{
			name: "压缩条目取摘要",
			raw:  `{"type":"compaction","id":"m5","summary":"压缩摘要"}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindCompaction || e.Text != "压缩摘要" {
					t.Fatalf("压缩投影异常: %+v", e)
				}
			},
		},
		{
			// 单字段类型错误不得毁掉其它字段：旧实现里工具名一坏，
			// isError 也读不出来（同一份严格解码整体失败）。
			name: "工具名类型错误只降级名称",
			raw:  `{"type":"message","id":"m6","message":{"role":"toolResult","toolName":7,"isError":true,"content":[{"type":"text","text":"输出"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindTool || e.ToolName != "" || !e.Failed || e.Text != "输出" {
					t.Fatalf("工具名降级应为空且其余字段保留: %+v", e)
				}
			},
		},
		{
			name: "用量类型错误只降级用量",
			raw:  `{"type":"message","id":"m7","message":{"role":"assistant","content":[{"type":"text","text":"答"}],"usage":5}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindAssistant || e.Text != "答" || e.Usage != nil {
					t.Fatalf("用量降级应保留正文: %+v", e)
				}
			},
		},
		{
			name: "用量为 null 视为没有记录",
			raw:  `{"type":"message","id":"m8","message":{"role":"assistant","content":[{"type":"text","text":"答"}],"usage":null}}`,
			check: func(t *testing.T, e Entry) {
				if e.Usage != nil {
					t.Fatalf("null 用量应为 nil: %+v", e.Usage)
				}
			},
		},
		{
			// 一个坏块不得丢掉整条消息的块清单：旧实现里 blocks 解码
			// 整体失败，同一条消息里合法的思考占位符也一起消失。
			name: "坏块不毁块清单",
			raw:  `{"type":"message","id":"m9","message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"},{"type":7},{"type":"text","text":"答"}]}}`,
			check: func(t *testing.T, e Entry) {
				if len(e.Lazy) != 1 || e.Lazy[0] != (LazyBlock{Kind: "thinking", BlockIndex: 0}) {
					t.Fatalf("坏块之后的合法占位符必须保留: %+v", e.Lazy)
				}
				if e.Text != "答" {
					t.Fatalf("坏块不得吞掉正文: %q", e.Text)
				}
			},
		},
		{
			// 旧实现里 role 类型错误会让「角色 + 正文」一起清零；
			// 正文是真实内容，必须保留（角色降级为未知）。
			name: "角色类型错误保留正文",
			raw:  `{"type":"message","id":"m10","message":{"role":7,"content":[{"type":"text","text":"x"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindOther || e.Text != "x" {
					t.Fatalf("角色降级为未知但正文应保留: %+v", e)
				}
			},
		},
		{
			name: "文本块里 text 为数字只跳过该块",
			raw:  `{"type":"message","id":"m11","message":{"role":"assistant","content":[{"type":"text","text":7},{"type":"text","text":"ok"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Text != "ok" {
					t.Fatalf("合法文本块应保留: %q", e.Text)
				}
			},
		},
		{
			name: "null 字段不 panic 且视为缺失",
			raw:  `{"type":"message","id":"m12","message":{"role":null,"content":null,"toolName":null,"usage":null}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindOther || e.Text != "" || e.Lazy != nil || e.Usage != nil {
					t.Fatalf("null 字段投影异常: %+v", e)
				}
			},
		},
		{
			// stopReason 已表明失败时即使 errorMessage 畸形也要给出通用提示，
			// 而不是把错误吞成空串。
			name: "错误消息类型异常仍显示通用错误",
			raw:  `{"type":"message","id":"m13","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":9}}`,
			check: func(t *testing.T, e Entry) {
				if e.Error == "" {
					t.Fatalf("stopReason=error 应给出错误提示: %+v", e)
				}
			},
		},
		{
			name: "未知角色归入其他且保留正文",
			raw:  `{"type":"message","id":"m14","message":{"role":"alien","content":[{"type":"text","text":"?"}]}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindOther || e.Text != "?" {
					t.Fatalf("未知角色投影异常: %+v", e)
				}
			},
		},
		{
			name: "旧式命令记录（无 role 有 command）推断为工具",
			raw:  `{"type":"message","id":"m15","message":{"command":"ls","output":"a.go"}}`,
			check: func(t *testing.T, e Entry) {
				if e.Kind != KindTool || e.Text != "ls" {
					t.Fatalf("旧式命令推断异常: %+v", e)
				}
			},
		},
		{
			name: "content 为字符串时无惰性块",
			raw:  `{"type":"message","id":"m16","message":{"role":"user","content":"纯文本"}}`,
			check: func(t *testing.T, e Entry) {
				if e.Lazy != nil {
					t.Fatalf("字符串 content 不应有惰性块: %+v", e.Lazy)
				}
			},
		},
		{
			name: "用户图片生成图片占位符",
			raw:  `{"type":"message","id":"m17","message":{"role":"user","content":[{"type":"text","text":"颜色？"},{"type":"image","mimeType":"image/png","data":"aGVsbG8="}]}}`,
			check: func(t *testing.T, e Entry) {
				if len(e.Lazy) != 1 || e.Lazy[0] != (LazyBlock{Kind: "image", BlockIndex: 1}) {
					t.Fatalf("用户图片占位符异常: %+v", e.Lazy)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries := ProjectEntries([]json.RawMessage{json.RawMessage(c.raw)})
			if len(entries) != 1 {
				t.Fatalf("应投影出一条条目: %+v", entries)
			}
			c.check(t, entries[0])
		})
	}
}
