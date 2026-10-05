// 收起态预览的有界渲染。
//
// 工作段默认折叠，预览行（summary 里的 tool-preview）是唯一始终可见的文本。
// 以前预览与展开详情渲染的是同一份完整工具正文：写文件类工具的正文可达
// 数 MB，实测预览把页面撑到 96.8% 的体积，而 CSS 只显示一行——渲染与传输
// 的都是看不见的内容。
//
// 现在收起预览只取前 previewRunes 个码点；展开详情（pre.tool-detail）、
// 复制与导出继续使用完整正文，不得被截断。
package presentation

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"pi-bridge-go/internal/sessions"
)

// previewSpan 从渲染结果里取出收起预览的文本内容。
func previewSpan(t *testing.T, html string) string {
	t.Helper()
	const open = `<span class="tool-preview">`
	start := strings.Index(html, open)
	if start < 0 {
		end := len(html)
		if end > 400 {
			end = 400
		}
		t.Fatalf("找不到预览元素：%s", html[:end])
	}
	rest := html[start+len(open):]
	end := strings.Index(rest, "</span>")
	if end < 0 {
		t.Fatal("预览元素未闭合")
	}
	return rest[:end]
}

// 长工具正文：预览只保留前 200 个码点并附省略号；全文只出现在详情里一次。
func Test收起预览有界而详情保留全文(t *testing.T) {
	renderer := testRenderer(t)
	long := strings.Repeat("汉", 3000) // 3000 码点 / 9000 字节
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"write","content":[{"type":"text","text":"` + long + `"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// 全文只在展开详情里出现一次；预览里不再有完整正文。
	if got := strings.Count(html, long); got != 1 {
		t.Fatalf("完整正文应只出现一次（详情），实际 %d 次", got)
	}
	if !strings.Contains(html, `<pre class="tool-detail">`+long+`</pre>`) {
		t.Fatal("展开详情必须保留未截断的完整正文")
	}
	preview := previewSpan(t, html)
	want := strings.Repeat("汉", 200) + "…"
	if preview != want {
		t.Fatalf("预览应为前 200 码点加省略号：len(preview)=%d runes=%d 前缀=%q", len(preview), utf8.RuneCountInString(preview), preview[:min(40, len(preview))])
	}
	if !utf8.ValidString(preview) {
		t.Fatal("预览必须按码点边界截断，不能切断多字节字符")
	}
}

// 短正文不截断、不加省略号：预览与全文一致。
func Test短预览不加省略号(t *testing.T) {
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
	if got := previewSpan(t, html); got != "ok 一切正常" {
		t.Fatalf("短正文预览不应截断：%q", got)
	}
}

// 恰好 200 码点的正文也不带省略号（截断只发生在真的还有内容时）。
func Test恰好边界不省略(t *testing.T) {
	renderer := testRenderer(t)
	exact := strings.Repeat("边", 200)
	html, err := renderer.RenderHistory("s1", sessions.Page{
		Entries: []json.RawMessage{
			json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"跑"}]}}`),
			json.RawMessage(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":"bash","content":[{"type":"text","text":"` + exact + `"}]}}`),
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if got := previewSpan(t, html); got != exact {
		t.Fatalf("恰好 %d 码点不应加省略号：得到 %d 码点", 200, utf8.RuneCountInString(got))
	}
}
