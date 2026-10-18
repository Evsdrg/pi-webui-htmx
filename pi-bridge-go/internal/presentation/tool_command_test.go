// 工具行必须显示「调用了什么」，而不只是「输出了什么」。
//
// 2026-10-18 实测（lab，DeepSeek V4.1 Flash）：一个回合里四条 bash 调用，
// 历史区的四行分别是 `bash 3`、`bash 0ISEkc…`、`write Successfully wrote…`、
// `read hello`——用户完全看不到执行的命令是什么。原因是投影只取 toolResult，
// 而 `bash` 的命令在**上一条 assistant 消息的 toolCall 参数**里（Content 只有
// `[{"type":"text","text":"3\n"}]`），两条记录之间靠 toolCallId 关联。
//
// 这里锁三件事：
//  1. 工具行（预览与详情）带出所属调用的参数预览（bash 是命令本身）；
//  2. 没有前序 toolCall 时仍按旧行为显示输出，不因缺关联而空白；
//  3. 关联按 toolCallId 精确匹配，并行调用不会互相串参数。
package presentation

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// 反例：bash 的命令来自前一条 assistant 的 toolCall 参数，历史行必须带出来。
func Test工具行显示调用的命令而不是只有输出(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[` +
				`{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"printf 'a\\nb\\nc\\n' | wc -l"}},` +
				`{"type":"toolCall","id":"call_2","name":"read","arguments":{"path":"/tmp/probe.txt"}}` +
				`]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"3\n"}]}}`),
			json.RawMessage(`{"type":"message","id":"t2","message":{"role":"toolResult","toolCallId":"call_2","toolName":"read","content":[{"type":"text","text":"hello"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	bash := blockByEntry(t, html, "t1")
	if !strings.Contains(bash, `printf &#39;a\nb\nc\n&#39; | wc -l`) && !strings.Contains(bash, `printf 'a\nb\nc\n' | wc -l`) {
		t.Fatalf("bash 行必须显示执行的命令（来自 toolCall 参数）：%s", bash)
	}
	read := blockByEntry(t, html, "t2")
	if !strings.Contains(read, `/tmp/probe.txt`) {
		t.Fatalf("read 行必须显示读取的路径：%s", read)
	}
	// 并行调用的参数不能串：bash 行里不应出现 read 的路径。
	if strings.Contains(bash, `/tmp/probe.txt`) {
		t.Fatalf("工具行串了别的调用的参数：%s", bash)
	}
}

// 没有前序 toolCall（老会话、被裁剪的历史窗口）时退回显示输出，不渲染空行。
func Test缺少调用参数时退回显示输出(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"ok 一切正常"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	block := blockByEntry(t, html, "t1")
	if !strings.Contains(block, "ok 一切正常") {
		t.Fatalf("缺调用参数时应显示输出：%s", block)
	}
}
