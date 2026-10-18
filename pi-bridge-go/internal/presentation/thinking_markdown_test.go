// 思考内容按 markdown 渲染。
//
// 实测语料（本机 3786 个非空思考块）：68% 含行内代码、46% 含有序列表、
// 44% 含加粗、17% 含围栏代码块。旧模板把思考正文塞进一个 white-space:pre-wrap
// 的纯文本块，这些语法全部以原始符号显示（`foo`、**重点**、``` 围栏）。
package presentation

import (
	"strings"
	"testing"
)

// 反例：思考正文必须落在 .markdown 容器里，前端渲染管线按这个类识别。
func Test思考正文输出markdown容器(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderThinking("先看 `main.go`，再改 **配置**")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "markdown") {
		t.Fatalf("思考正文应放在 .markdown 容器里交给前端渲染：%s", html)
	}
	// 源文本仍要原样进页面（前端读 textContent 再渲染），不能被转义成实体后丢失。
	if !strings.Contains(html, "`main.go`") {
		t.Fatalf("markdown 源文本应原样输出：%s", html)
	}
	// 模板自动转义：思考内容是模型输出，不能直接当 HTML 注入。
	html2, err := renderer.RenderThinking("</div><img src=x onerror=alert(1)>")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html2, "<img src=x") {
		t.Fatalf("思考正文必须转义，不能输出可执行 HTML：%s", html2)
	}
}

// 空思考仍有占位，不留空白块。
func Test空思考有占位(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderThinking("")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(html, "空") {
		t.Fatalf("空思考应显示占位：%s", html)
	}
}
