package management

import (
	"encoding/json"
	"strings"
	"testing"
)

// MCP 配置域的读取、脱敏与校验。探测走 CLI，单测只覆盖纯函数部分
// （探测本身需要真实 pi 可执行文件，属集成行为）。
func TestMCP读取与脱敏(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "mcp.json", `{
	  "mcpServers": {
	    "fs": {"command": "npx", "args": ["-y", "server-filesystem"], "env": {"TOKEN": "sk-secret-value"}},
	    "docs": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer docs-secret"}}
	  }
	}`)
	reply, err := NewConfig(dir, DefaultLimits()).MCP()
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Servers) != 2 {
		t.Fatalf("应有两台服务器，实际 %d", len(reply.Servers))
	}
	byName := map[string]MCPServerInfo{}
	for _, s := range reply.Servers {
		byName[s.Name] = s
	}
	fs := byName["fs"]
	if fs.Transport != "npx -y server-filesystem" {
		t.Errorf("stdio 传输应展示命令与参数，实际 %q", fs.Transport)
	}
	if !fs.Enabled || fs.Exposure != "codemode" || fs.Scope != "global" {
		t.Errorf("默认值不对：%+v", fs)
	}
	if byName["docs"].Transport != "https://example.com/mcp" {
		t.Errorf("http 传输应展示 URL，实际 %q", byName["docs"].Transport)
	}

	// 秘密必须被打码，且原值不出现在文档里。
	servers := reply.Document["mcpServers"].(map[string]any)
	fsEntry := servers["fs"].(map[string]any)
	if env := fsEntry["env"].(map[string]any); env["TOKEN"] != "***" {
		t.Errorf("env 值必须打码，实际 %v", env["TOKEN"])
	}
	docsEntry := servers["docs"].(map[string]any)
	if h := docsEntry["headers"].(map[string]any); h["Authorization"] != "***" {
		t.Errorf("headers 值必须打码，实际 %v", h["Authorization"])
	}
	blob, _ := marshalString(reply.Document)
	for _, secret := range []string{"sk-secret-value", "docs-secret"} {
		if strings.Contains(blob, secret) {
			t.Errorf("文档里不得残留秘密 %q：%s", secret, blob)
		}
	}
}

func TestMCP文件不存在返回空(t *testing.T) {
	reply, err := NewConfig(t.TempDir(), DefaultLimits()).MCP()
	if err != nil {
		t.Fatalf("缺文件不该报错：%v", err)
	}
	if len(reply.Servers) != 0 || len(reply.Errors) != 0 {
		t.Fatalf("缺文件应得到空清单：%+v", reply)
	}
	if _, ok := reply.Document["mcpServers"]; !ok {
		t.Errorf("空文档也应带上 mcpServers 键：%+v", reply.Document)
	}
}

func TestMCP非法条目被跳过并回报(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "mcp.json", `{
	  "mcpServers": {
	    "good": {"url": "https://example.com/mcp"},
	    "bad-name!": {"url": "https://example.com/mcp"},
	    "no-transport": {"description": "既无命令也无 URL"}
	  }
	}`)
	reply, err := NewConfig(dir, DefaultLimits()).MCP()
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Servers) != 1 || reply.Servers[0].Name != "good" {
		t.Fatalf("只应保留合法条目：%+v", reply.Servers)
	}
	if len(reply.Errors) != 2 {
		t.Fatalf("应回报 2 条配置错误，实际 %v", reply.Errors)
	}
	// 非法条目仍保留在脱敏文档里，便于用户在原位修复。
	if _, ok := reply.Document["mcpServers"].(map[string]any)["bad-name!"]; !ok {
		t.Errorf("非法条目也应留在文档里：%+v", reply.Document)
	}
}

func TestMCP写入保留秘密(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "mcp.json", `{
	  "mcpServers": {
	    "docs": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer keep-me"}}
	  }
	}`)
	c := NewConfig(dir, DefaultLimits())
	// 提交时带 *** 表示沿用原值。
	if err := c.WriteMCP(map[string]any{
		"mcpServers": map[string]any{
			"docs": map[string]any{
				"url":     "https://example.com/mcp",
				"headers": map[string]any{"Authorization": "***"},
			},
		},
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	raw, err := c.read("mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Bearer keep-me") {
		t.Fatalf("*** 应还原成磁盘原值：%s", raw)
	}
}

func TestMCP拒绝新增命令型凭据(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "mcp.json", `{"mcpServers":{"docs":{"url":"https://example.com/mcp"}}}`)
	c := NewConfig(dir, DefaultLimits())
	err := c.WriteMCP(map[string]any{
		"mcpServers": map[string]any{
			"docs": map[string]any{
				"url":     "https://example.com/mcp",
				"headers": map[string]any{"Authorization": "!cat /etc/secret"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "命令型凭据") {
		t.Fatalf("网页新增命令型凭据必须被拒绝，实际 %v", err)
	}
}

func TestMCP校验非法字段(t *testing.T) {
	cases := []struct {
		name  string
		entry map[string]any
	}{
		{"既无命令也无URL", map[string]any{"description": "x"}},
		{"非法曝光策略", map[string]any{"url": "https://e.com/mcp", "exposure": "loud"}},
		{"非法type", map[string]any{"url": "https://e.com/mcp", "type": "sse"}},
		{"timeout非整数", map[string]any{"url": "https://e.com/mcp", "timeout": 1.5}},
		{"args非字符串", map[string]any{"command": "npx", "args": []any{1}}},
		{"env值非字符串", map[string]any{"command": "npx", "env": map[string]any{"K": 1}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := validateMCPServer("srv", c.entry); msg == "" {
				t.Fatalf("应判为非法：%+v", c.entry)
			}
		})
	}
	if msg := validateMCPServer("bad name", map[string]any{"url": "https://e.com/mcp"}); msg == "" {
		t.Error("名称含空格应判为非法")
	}
	if msg := validateMCPServer("ok", map[string]any{"url": "https://e.com/mcp", "toolExposure": map[string]any{"a": "direct"}}); msg != "" {
		t.Errorf("合法条目不该报错：%s", msg)
	}
}

// marshalString 是测试用的稳定序列化，便于断言里搜子串。
func marshalString(value any) (string, error) {
	body, err := json.Marshal(value)
	return string(body), err
}
