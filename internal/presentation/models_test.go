package presentation

import (
	"strings"
	"testing"
)

// 模型选择器靠 printf 比较 provider/id 来决定 selected。
// 自定义 printf 覆盖被删除后（见 fragments_test.go）这里必须保持不变——
// 那是被删函数唯一的真实用途。
func Test模型选择器selected仍正确(t *testing.T) {
	r := testRenderer(t)
	html, err := r.execute("models.html", ModelsData{
		Current: "CPA-Responses/deepseek-flash",
		Models: []ModelRow{
			{Provider: "CPA-Responses", ID: "deepseek-flash", Name: "DeepSeek Flash"},
			{Provider: "CPA-Messages", ID: "MiniMax-M3", Name: "MiniMax M3"},
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	first := html[strings.Index(html, "CPA-Responses/deepseek-flash"):]
	if !strings.Contains(first[:strings.Index(first, "</option>")+9], "selected") {
		t.Fatalf("当前模型应带 selected：%s", html)
	}
	rest := html[strings.Index(html, "MiniMax-M3"):]
	if strings.Contains(rest[:strings.Index(rest, "</option>")+9], "selected") {
		t.Fatalf("非当前模型不应带 selected：%s", html)
	}
}
