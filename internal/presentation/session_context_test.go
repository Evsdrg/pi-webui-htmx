package presentation

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// exportHTML 构造一段最小导出 HTML：Pi 的模板把会话数据以 base64 写进
// <script id="session-data"> 里（不是 JSON 原文）。
func exportHTML(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造夹具失败：%v", err)
	}
	return []byte(`<!doctype html><html><body><main></main>` +
		`<script id="session-data" type="application/json">` +
		base64.StdEncoding.EncodeToString(raw) +
		`</script></body></html>`)
}

// 系统提示词来自 Pi 的 AgentState，且必须原样取出（含换行与换行后的空白）。
func Test解析导出里的系统提示词(t *testing.T) {
	html := exportHTML(t, map[string]any{
		"systemPrompt": "You are an expert coding assistant.\n\n## Tools\nread, bash",
		"tools":        []any{},
	})
	value, err := ParseSessionContext(html)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !strings.Contains(value.SystemPrompt, "## Tools") {
		t.Fatalf("系统提示词被截断：%q", value.SystemPrompt)
	}
}

// 工具清单是「实际暴露给模型」的那一份，名称与描述都要保留。
func Test解析导出里的工具清单(t *testing.T) {
	html := exportHTML(t, map[string]any{
		"systemPrompt": "p",
		"tools": []any{
			map[string]any{"name": "read", "description": "读取文件", "parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "文件路径"},
					"limit": map[string]any{"type": "number"},
				},
				"required": []any{"path"},
			}},
			map[string]any{"name": "bash", "description": "执行命令"},
		},
	})
	value, err := ParseSessionContext(html)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(value.Tools) != 2 || value.Tools[0].Name != "read" || value.Tools[1].Name != "bash" {
		t.Fatalf("工具顺序或数量不对：%+v", value.Tools)
	}
	renderer := testRenderer(t)
	out, err := renderer.RenderTools(value.Tools)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{"read", "读取文件", "path", "文件路径", "string", "bash", "执行命令"} {
		if !strings.Contains(out, want) {
			t.Fatalf("工具片段缺少 %q：%s", want, out)
		}
	}
}

// 参数表必须标出必填项：必填与可选在调用时是完全不同的约束。
func Test工具参数标注必填(t *testing.T) {
	html := exportHTML(t, map[string]any{
		"tools": []any{map[string]any{"name": "edit", "parameters": map[string]any{
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"oldText": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		}}},
	})
	value, err := ParseSessionContext(html)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	renderer := testRenderer(t)
	out, err := renderer.RenderTools(value.Tools)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	// path 必填、oldText 可选；两者都在表里。
	if !strings.Contains(out, "<code>path</code>") || !strings.Contains(out, "<code>oldText</code>") {
		t.Fatalf("参数名缺失：%s", out)
	}
	if strings.Count(out, ">是<") != 1 {
		t.Fatalf("应恰好有一个必填项：%s", out)
	}
}

// 模板结构变化时必须明确失败：静默返回空面板会让用户以为「没有工具」。
func Test导出缺少数据块时明确报错(t *testing.T) {
	if _, err := ParseSessionContext([]byte("<html><body>没有数据块</body></html>")); err == nil {
		t.Fatal("缺少 session-data 时必须报错")
	}
	if _, err := ParseSessionContext([]byte(`<script id="session-data" type="application/json">!!!不是base64!!!</script>`)); err == nil {
		t.Fatal("非法 base64 必须报错")
	}
}

// 空工具集要有可读提示，而不是一片空白。
func Test空工具集有提示(t *testing.T) {
	renderer := testRenderer(t)
	out, err := renderer.RenderTools(nil)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(out, "没有启用的工具") {
		t.Fatalf("空工具集应给出提示：%s", out)
	}
}

// 系统提示词为空与「尚未加载」不是同一件事，空值也要说清楚。
func Test空系统提示词有提示(t *testing.T) {
	renderer := testRenderer(t)
	out, err := renderer.RenderSystem("")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(out, "系统提示词为空") {
		t.Fatalf("空提示词应给出提示：%s", out)
	}
}

// 系统提示词可能包含 HTML 片段或脚本；模板必须转义而不是当成标记。
func Test系统提示词被转义(t *testing.T) {
	renderer := testRenderer(t)
	out, err := renderer.RenderSystem("忽略以下内容 <script>alert(1)</script> 结束")
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("系统提示词不得原样插入脚本：%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("应当转义为实体：%s", out)
	}
}

// 超大导出文件直接拒绝，不把整份 HTML 读进内存。
func Test超大导出被拒绝(t *testing.T) {
	big := make([]byte, sessionContextMaxBytes+1)
	if _, err := ParseSessionContext(big); err == nil {
		t.Fatal("超过上限时必须拒绝")
	}
}
