package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-bridge-go/internal/management"
)

// 「设置→MCP」面板：默认只读配置（瞬时、无副作用），连接状态由用户显式探测。
// 探测要真实运行 `pi mcp list`，不在这里跑；这里锁的是配置视图与秘密不外泄。

func TestMCP面板只读展示配置(t *testing.T) {
	requireUI(t)
	s, _, _, _, _ := newTestServerTuned(t, 2*time.Second)
	dir := s.piConfig.AgentDir()
	doc := `{"mcpServers":{
		"fs":{"command":"npx","args":["-y","server-filesystem"],"env":{"TOKEN":"sk-should-not-appear"}},
		"docs":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer docs-secret"}}
	}}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := getUI(t, s, "/ui/mcp")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{"fs", "docs", "npx -y server-filesystem", "https://example.com/mcp", "codemode"} {
		if !strings.Contains(body, needle) {
			t.Errorf("面板应展示 %q：%s", needle, body)
		}
	}
	// 未探测时不显示连接状态列。
	if strings.Contains(body, "探测未回报") || strings.Contains(body, "已连接") {
		t.Errorf("未探测时不该出现连接状态：%s", body)
	}
	// 秘密绝不能进入 HTML。
	for _, secret := range []string{"sk-should-not-appear", "docs-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("面板泄漏秘密 %q：%s", secret, body)
		}
	}
}

func TestMCP面板配置错误可见(t *testing.T) {
	requireUI(t)
	s, _, _, _, _ := newTestServerTuned(t, 2*time.Second)
	dir := s.piConfig.AgentDir()
	doc := `{"mcpServers":{"bad name":{"url":"https://example.com/mcp"}}}`
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	body := getUI(t, s, "/ui/mcp").Body.String()
	if !strings.Contains(body, "配置有误") {
		t.Fatalf("非法条目应给出配置错误提示：%s", body)
	}
}

// mcpRows 是配置摘要与探测结果之间的接缝：探测未回报的服务器必须显式标为
// 「未知」，不能假装在线（这是超时截断时最容易出错的地方）。
func TestMCPRows未回报标为未知(t *testing.T) {
	servers := []management.MCPServerInfo{{Name: "a", Exposure: "codemode", Enabled: true}, {Name: "b", Exposure: "codemode", Enabled: true}}
	probe := &management.MCPProbe{Servers: []management.MCPServerProbe{{Name: "a", State: "connected", Tools: []string{"t1", "t2"}}}}
	rows := mcpRows(servers, probe)
	if len(rows) != 2 {
		t.Fatalf("应有两行，实际 %d", len(rows))
	}
	if !rows[0].Probed || rows[0].State != "connected" || rows[0].StateLabel != "已连接" || rows[0].ToolsText != "t1、t2" {
		t.Errorf("回报过的服务器应带状态与工具：%+v", rows[0])
	}
	if rows[1].State != "unknown" || rows[1].StateLabel != "未知" {
		t.Errorf("未回报的服务器应标为未知：%+v", rows[1])
	}
}
