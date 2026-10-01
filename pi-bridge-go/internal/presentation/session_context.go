package presentation

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"

	"pi-bridge-go/internal/protocol"
)

// sessionContextMaxBytes 是允许解析的导出 HTML 上限。
// 导出文件本身可达数十 MiB，但只有内嵌的 session-data 对我们有用；
// 超过上限直接拒绝，避免为一个面板把整份 HTML 读进内存。
const sessionContextMaxBytes = 64 << 20

// sessionDataScript 匹配 Pi 导出模板里的会话数据脚本块。
// 模板写入的是 base64（不是 JSON），因此这里按 base64 解回。
var sessionDataScript = regexp.MustCompile(`(?s)<script id="session-data" type="application/json">([^<]*)</script>`)

// ToolInfo 是暴露给模型的工具定义。
//
// 它来自 Pi 的 AgentState.tools —— 也就是**当前实际生效**的工具集合，
// 受启动时的工具预设与扩展追加影响，不是"全部可用工具的目录"。
type ToolInfo struct {
	Name        string
	Description string
	// Parameters 是 JSON Schema 原文，渲染时再挑出顶层字段。
	Parameters json.RawMessage
}

// SessionContext 是导出文件里可供界面使用的那部分活动会话元数据。
//
// 为什么绕这一圈：Pi 的 RPC 没有暴露系统提示词和工具定义的命令
// （rpc-mode 的命令表里只有 get_state/get_session_stats 等，均不含这两项）。
// 唯一带着它们的出口是 export_html —— 它把 AgentState 里的 systemPrompt
// 与 tools 一并写进 HTML。因此桥按需导出一次、从中取回，而不是假装能直接问。
type SessionContext struct {
	SystemPrompt string
	Tools        []ToolInfo
}

// ParseSessionContext 从导出的 HTML 里取回系统提示词与工具定义。
func ParseSessionContext(html []byte) (SessionContext, error) {
	if len(html) > sessionContextMaxBytes {
		return SessionContext{}, protocol.E("limit_exceeded", "导出文件过大，无法读取会话元数据")
	}
	match := sessionDataScript.FindSubmatch(html)
	if match == nil {
		// 模板结构变了就该明确失败，而不是静默给一个空面板。
		return SessionContext{}, protocol.E("internal", "导出文件里没有会话数据块")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(match[1])))
	if err != nil {
		return SessionContext{}, protocol.E("internal", "会话数据不是合法 base64")
	}
	var payload struct {
		SystemPrompt string `json:"systemPrompt"`
		Tools        []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return SessionContext{}, protocol.E("internal", "会话数据不是合法 JSON")
	}
	tools := make([]ToolInfo, 0, len(payload.Tools))
	for _, tool := range payload.Tools {
		if tool.Name == "" {
			continue
		}
		tools = append(tools, ToolInfo{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return SessionContext{SystemPrompt: payload.SystemPrompt, Tools: tools}, nil
}

// ToolParam 是工具参数表里的一项。
type ToolParam struct {
	Name     string
	Type     string
	Required bool
	Desc     string
	// Enum 与 Default 直接来自 JSON Schema，有才展示。
	Enum    string
	Default string
}

// ToolView 是一个工具的完整定义。
type ToolView struct {
	Name        string
	Description string
	Params      []ToolParam
}

// ToolsData 驱动工具定义面板。
//
// 结构对齐 Pi Web 的 ToolDefinitionsPanel：左栏是工具名列表，右栏是所选
// 工具的详情。全部详情都渲染进 HTML，点击只切换可见性——切换是瞬时交互
// 状态，不该为它再发一次请求（那会重新导出一次会话快照）。
type ToolsData struct {
	Tools []ToolView
}

// RenderTools 渲染工具定义片段。
func (r *Renderer) RenderTools(tools []ToolInfo) (string, error) {
	views := make([]ToolView, 0, len(tools))
	for _, tool := range tools {
		views = append(views, ToolView{
			Name:        tool.Name,
			Description: strings.TrimSpace(tool.Description),
			Params:      toolParams(tool.Parameters),
		})
	}
	return r.execute("tools.html", ToolsData{Tools: views})
}

// toolParams 从 JSON Schema 里挑出顶层属性。
// 只做一层：嵌套的 anyOf/oneOf 交给类型名展示，不递归展开成表格。
func toolParams(schema json.RawMessage) []ToolParam {
	if len(schema) == 0 {
		return nil
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(schema, &parsed) != nil || len(parsed.Properties) == 0 {
		return nil
	}
	required := map[string]bool{}
	for _, name := range parsed.Required {
		required[name] = true
	}
	// 属性顺序在 JSON 对象里不稳定，按名字排序让输出可复现。
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sortStrings(names)
	params := make([]ToolParam, 0, len(names))
	for _, name := range names {
		var field struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
			Enum        []any  `json:"enum"`
			AnyOf       []any  `json:"anyOf"`
			Default     any    `json:"default"`
		}
		_ = json.Unmarshal(parsed.Properties[name], &field)
		param := ToolParam{
			Name:     name,
			Type:     schemaType(field.Type, field.Enum, field.AnyOf),
			Required: required[name],
			Desc:     strings.TrimSpace(field.Description),
		}
		if len(field.Enum) > 0 {
			values := make([]string, 0, len(field.Enum))
			for _, value := range field.Enum {
				values = append(values, literalText(value))
			}
			param.Enum = strings.Join(values, ", ")
		}
		if field.Default != nil {
			param.Default = literalText(field.Default)
		}
		params = append(params, param)
	}
	return params
}

// schemaType 把 JSON Schema 的类型写法压成一个短标签。
func schemaType(raw any, enum []any, anyOf []any) string {
	if len(anyOf) > 0 {
		parts := make([]string, 0, len(anyOf))
		for _, variant := range anyOf {
			if m, ok := variant.(map[string]any); ok {
				parts = append(parts, schemaType(m["type"], nil, nil))
			}
		}
		if len(parts) > 0 {
			return strings.Join(uniqueStrings(parts), " | ")
		}
	}
	if len(enum) > 0 {
		return "enum"
	}
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(uniqueStrings(parts), " | ")
		}
	}
	return ""
}

// literalText 把 JSON Schema 里的字面值渲染成短文本。
// 字符串按原样（枚举值就是给用户看的），其余走 JSON。
func literalText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// SystemData 驱动系统提示词片段。
type SystemData struct {
	Prompt string
}

// RenderSystem 渲染系统提示词片段。
func (r *Renderer) RenderSystem(prompt string) (string, error) {
	return r.execute("system.html", SystemData{Prompt: prompt})
}
