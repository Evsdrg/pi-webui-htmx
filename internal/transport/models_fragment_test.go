package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/presentation"
)

// 这条锁的是「信封」与「载荷」之间的那道接缝：`management.Config.Models()`
// 返回的 `ModelsReply.Providers` 已经是 providers 子树，而投影函数要的正是
// 这个子树——不是整份文档。
//
// K2 批次把回执从匿名 map 改成具名类型时，调用点从 `ConfigModels(out)` 变成
// `ConfigModels(out.Providers)`，但函数内部还在取 `doc["providers"]`，
// 于是永远取到 nil：模型选择器空着，看上去像「用户没配模型」——不报错、
// 不显眼，只有真去选模型时才发现。只测投影函数或只测回执都发现不了，
// 必须把两者接起来测。
func Test模型清单从配置文件一路读到选项(t *testing.T) {
	uiDir := os.Getenv("PI_WEBUI_DIR")
	if uiDir == "" {
		t.Skip("需要 PI_WEBUI_DIR 加载 UI 包")
	}
	dir := t.TempDir()
	doc := `{"providers":{
		"CPA-Responses":{"api":"openai-responses","baseUrl":"http://127.0.0.1:1/v1",
			"apiKey":"sk-should-not-appear",
			"models":[{"id":"deepseek-flash","name":"DeepSeek Flash"}]},
		"CPA-Messages":{"api":"anthropic-messages",
			"models":[{"id":"MiniMax-M3","name":"MiniMax M3"}]}
	}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	reply, err := management.NewConfig(dir, management.DefaultLimits()).Models()
	if err != nil {
		t.Fatalf("读取模型配置失败：%v", err)
	}
	if reply.ModelCount != 2 {
		t.Fatalf("应读到 2 个模型，实际 %d", reply.ModelCount)
	}

	rows := presentation.ConfigModels(reply.Providers)
	if len(rows) != 2 {
		t.Fatalf("投影后应有 2 个模型（0 个 = 信封/载荷混用的老毛病），实际 %d：%v", len(rows), rows)
	}

	r, err := presentation.LoadFromDir(uiDir)
	if err != nil {
		t.Fatalf("加载 UI 包失败：%v", err)
	}
	html, err := r.RenderModels(rows, "")
	if err != nil {
		t.Fatalf("渲染模型选择器失败：%v", err)
	}
	for _, needle := range []string{"CPA-Responses/deepseek-flash", "DeepSeek Flash", "MiniMax M3"} {
		if !strings.Contains(html, needle) {
			t.Errorf("选择器里应出现 %q：%s", needle, html)
		}
	}
	// 模型清单是展示投影，凭据不得跟着出来。
	if strings.Contains(html, "sk-should-not-appear") || strings.Contains(html, "apiKey") {
		t.Errorf("模型选择器不得带出凭据：%s", html)
	}
}
