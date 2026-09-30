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

// 模型清单的投影函数收的是 providers 子树，不是整份文档。
//
// 这条是真实故障的回归：K2 批次把回执改成具名类型后，调用点传了
// `reply.Providers`，而函数内部仍去找 `doc["providers"]`——于是永远取到
// nil。表现是模型选择器空着，看上去像"用户没配模型"：不报错、不显眼，
// 只有真正去选模型时才发现。断言直接用 providers 子树的形状，谁能通过、
// 谁不能通过一眼可见。
func Test模型清单投影收providers子树(t *testing.T) {
	providers := map[string]any{
		"CPA-Responses": map[string]any{
			"api": "openai-responses",
			"models": []any{
				map[string]any{"id": "deepseek-flash", "name": "DeepSeek Flash"},
				map[string]any{"id": "grok-4.7"},
			},
		},
		"CPA-Messages": map[string]any{
			// 旧桥的对象写法也要兼容。
			"models": map[string]any{
				"MiniMax-M3": map[string]any{"name": "MiniMax M3"},
			},
		},
	}
	rows := ConfigModels(providers)
	if len(rows) != 3 {
		t.Fatalf("应投影出 3 个模型，实际 %d：%v", len(rows), rows)
	}
	// 供应商按名字排序，模型保持配置里的顺序。
	want := []struct{ provider, id, name string }{
		{"CPA-Messages", "MiniMax-M3", "MiniMax M3"},
		{"CPA-Responses", "deepseek-flash", "DeepSeek Flash"},
		{"CPA-Responses", "grok-4.7", "grok-4.7"},
	}
	for i, w := range want {
		got := rows[i]
		if got["provider"] != w.provider || got["id"] != w.id || got["name"] != w.name {
			t.Errorf("第 %d 项应为 %s/%s(%s)，实际 %v", i, w.provider, w.id, w.name, got)
		}
	}
	// 没有 name 时退回 id，不显示空标签。
	if rows[2]["name"] != "grok-4.7" {
		t.Errorf("缺 name 的模型应退回 id，实际 %v", rows[2]["name"])
	}
}
