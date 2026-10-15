package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
)

// MCP 配置域：全局 mcp.json 的只读视图与受控写入，外加按需的连接探测。
//
// 与 models 域的关键差别：Pi 1.2.0 没有 MCP 专用 RPC，唯一机器可读入口是
// CLI `pi mcp list --json`，而它**会真实连接**每个启用的服务器（stdio 拉起
// 子进程、http 发请求，每服务器上限 60s，无缓存）。因此把「读配置」（瞬时、
// 无副作用）与「探测连接」（慢、有副作用）拆成两条路径，探测只由用户显式触发。
//
// 作用域只覆盖全局 ~/.pi/agent/mcp.json。项目级 .pi/mcp.json 受 project trust
// 限制，而桥运行时本就拒绝项目信任，让网页写项目配置要么无效、要么等于绕过
// 信任边界，所以不提供。
const (
	probeTimeout  = 20 * time.Second
	maxMCPServers = 128
	mcpNameMaxLen = 128
)

// ProbeBudget 返回单次 MCP 探测的最长耗时。
// HTTP 处理器要据此延长写截止时间——桥的默认 WriteTimeout 只有 15 秒，
// 探测可能跑满 20 秒，不改截止时间的话响应会在写回前就被掐断（实测：
// 客户端等到 20 秒却收到空正文）。
func ProbeBudget() time.Duration { return probeTimeout }

// mcpNamePattern 与 Pi 的服务器名规则一致（mcp-servers.js）。
var mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// mcpExposures 是 Pi 支持的四种曝光策略。
var mcpExposures = map[string]bool{"codemode": true, "deferred": true, "direct": true, "hidden": true}

// MCPServerInfo 是 config.mcp 回执里的一台服务器摘要。
// 只描述配置，不含连接状态——状态来自 ProbeMCP。
type MCPServerInfo struct {
	Name         string            `json:"name"`
	Transport    string            `json:"transport"`
	Enabled      bool              `json:"enabled"`
	Exposure     string            `json:"exposure"`
	ToolExposure map[string]string `json:"toolExposure,omitempty"`
	Description  string            `json:"description,omitempty"`
	Scope        string            `json:"scope"`
	Source       string            `json:"source"`
}

// MCPReply 是 config.mcp 的回执。
//
// Document 是脱敏后的用户文档子树，形状由用户的 mcp.json 决定，因此保持
// map[string]any——固定的只是外层信封（与 ModelsReply 同一立场）。
type MCPReply struct {
	Servers  []MCPServerInfo `json:"servers"`
	Document map[string]any  `json:"document"`
	Errors   []string        `json:"errors"`
	Note     string          `json:"note,omitempty"`
}

// MCPServerProbe 是 pi mcp list --json 里一台服务器的连接状态。
type MCPServerProbe struct {
	Name         string
	State        string
	Tools        []string
	ToolExposure map[string]string
	Resources    bool
	Error        string
}

// MCPProbe 是一次探测的汇总。
type MCPProbe struct {
	Servers []MCPServerProbe
	Errors  []string
	Note    string
}

// MCP 返回全局 mcp.json 的脱敏文档与结构摘要。
// 文件不存在时返回空文档而非报错——「还没配 MCP」是正常状态。
func (c *Config) MCP() (MCPReply, error) {
	doc, err := c.readObject("mcp.json")
	if os.IsNotExist(err) {
		doc = map[string]any{}
	} else if err != nil {
		return MCPReply{}, err
	}
	document, servers, errs, err := redactMCPDocument(doc)
	if err != nil {
		return MCPReply{}, err
	}
	return MCPReply{Servers: servers, Document: document, Errors: errs}, nil
}

// WriteMCP 校验副本、按服务器名保留秘密，再可靠替换全局 mcp.json。
// 与 WriteModels 同理：v1 的 *** 只表示保留已存在的秘密。
func (c *Config) WriteMCP(doc map[string]any) error {
	if doc == nil {
		return protocol.E("invalid_params", "配置不能为空")
	}
	input, err := json.Marshal(doc)
	if err != nil {
		return protocol.Wrap("invalid_params", "配置无法序列化", err)
	}
	if int64(len(input)) > c.limits.MaxFileBytes {
		return protocol.E("limit_exceeded", "配置超过体积上限")
	}
	var want map[string]any
	if err := json.Unmarshal(input, &want); err != nil {
		return protocol.E("invalid_params", "配置不是合法 JSON")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	original, err := c.readObject("mcp.json")
	if err != nil {
		if !os.IsNotExist(err) {
			var e *protocol.Error
			if !errors.As(err, &e) || e.Code != "invalid_history" {
				return err
			}
		}
		// 可用明确的新值修复坏文档，但不能猜测其中的秘密。
		original = nil
	}
	merged, err := restoreMCPSecrets(want, original)
	if err != nil {
		return err
	}
	if err := c.validateMCPDocument(merged); err != nil {
		return err
	}
	body, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return protocol.Wrap("invalid_params", "配置无法序列化", err)
	}
	body = append(body, '\n')
	if int64(len(body)) > c.limits.MaxFileBytes {
		return protocol.E("limit_exceeded", "配置超过体积上限")
	}
	return c.replaceConfig("mcp.json", body)
}

// ProbeMCP 运行 `pi mcp list --json` 取回各服务器的连接状态。
//
// 这会真实连接每个启用的服务器，因此只由用户显式触发，并受上下文超时约束。
// 退出码被忽略：非零既可能是「某服务器未连接」，也可能是 CLI 用法错误，
// 以 stdout 能否解析出 JSON 为准。
func (c *Config) ProbeMCP(ctx context.Context) (MCPProbe, error) {
	if c.piBin == "" {
		return MCPProbe{}, protocol.E("pi_error", "桥未配置 Pi 可执行文件路径，无法探测 MCP")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// argv 固定，不拼接任何用户输入；agent 目录经环境变量对齐（CLI 无 --agent-dir）。
	cmd := exec.CommandContext(ctx, c.piBin, "mcp", "list", "--json")
	if c.workspace != "" {
		cmd.Dir = c.workspace
	}
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+c.agentDir)
	// 独立进程组：`pi mcp list` 会为每个 stdio 服务器拉起子进程，超时取消时
	// 必须整组回收，否则会把 npx/servers 留成孤儿。
	prepareProbeProcess(cmd)
	cmd.WaitDelay = 250 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	killProbeGroup(cmd)

	var raw struct {
		Servers []struct {
			Name         string            `json:"name"`
			State        string            `json:"state"`
			Tools        []string          `json:"tools"`
			ToolExposure map[string]string `json:"toolExposure"`
			Resources    bool              `json:"resources"`
			Error        string            `json:"error"`
		} `json:"servers"`
		Errors []string `json:"errors"`
		Note   string   `json:"note"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		if ctx.Err() != nil {
			return MCPProbe{}, protocol.E("timeout", "探测 MCP 服务器超时，请稍后重试")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" && runErr != nil {
			detail = runErr.Error()
		}
		if detail != "" {
			return MCPProbe{}, protocol.E("pi_error", "无法读取 MCP 状态："+firstLine(detail))
		}
		return MCPProbe{}, protocol.E("pi_error", "无法读取 MCP 状态")
	}

	secrets := c.mcpSecretValues()
	probe := MCPProbe{Errors: make([]string, 0, len(raw.Errors)), Note: redactKnownSecrets(raw.Note, secrets)}
	for _, item := range raw.Servers {
		probe.Servers = append(probe.Servers, MCPServerProbe{
			Name:         item.Name,
			State:        item.State,
			Tools:        item.Tools,
			ToolExposure: item.ToolExposure,
			Resources:    item.Resources,
			Error:        redactKnownSecrets(item.Error, secrets),
		})
	}
	for _, item := range raw.Errors {
		probe.Errors = append(probe.Errors, redactKnownSecrets(item, secrets))
	}
	return probe, nil
}

// SetPiBinary 记录 Pi 可执行文件路径，供 MCP 探测使用。
func (c *Config) SetPiBinary(path string) { c.piBin = path }

// SetWorkspace 记录工作区根，作为 MCP 探测的工作目录。
func (c *Config) SetWorkspace(dir string) { c.workspace = dir }

// redactMCPDocument 返回脱敏文档、有效服务器摘要与非法条目的错误说明。
// 非法条目仍以脱敏形式保留在文档里，便于用户在编辑界面修复。
func redactMCPDocument(doc map[string]any) (map[string]any, []MCPServerInfo, []string, error) {
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok && doc["mcpServers"] != nil {
		return nil, nil, nil, protocol.E("invalid_history", "mcpServers 必须是对象")
	}
	out := make(map[string]any, len(doc)+1)
	for key, value := range doc {
		out[key] = value
	}
	redacted := make(map[string]any, len(servers))
	infos := make([]MCPServerInfo, 0, len(servers))
	errs := make([]string, 0)
	for _, name := range sortedKeys(servers) {
		entry, ok := servers[name].(map[string]any)
		if !ok {
			errs = append(errs, name+": 服务器条目必须是对象")
			redacted[name] = servers[name]
			continue
		}
		redacted[name] = redactMCPServer(entry)
		if msg := validateMCPServer(name, entry); msg != "" {
			errs = append(errs, name+": "+msg)
			continue
		}
		infos = append(infos, mcpServerInfo(name, entry))
	}
	out["mcpServers"] = redacted
	return out, infos, errs, nil
}

// redactMCPServer 返回条目的副本，把 env / headers 的全部值与 oauth.clientSecret 打码。
func redactMCPServer(entry map[string]any) map[string]any {
	out := make(map[string]any, len(entry))
	for key, value := range entry {
		out[key] = value
	}
	for _, field := range []string{"env", "headers"} {
		if m, ok := entry[field].(map[string]any); ok {
			masked := make(map[string]any, len(m))
			for key := range m {
				masked[key] = secretPlaceholder
			}
			out[field] = masked
		}
	}
	if oauth, ok := entry["oauth"].(map[string]any); ok {
		copied := make(map[string]any, len(oauth))
		for key, value := range oauth {
			copied[key] = value
		}
		if _, exists := oauth["clientSecret"]; exists {
			copied["clientSecret"] = secretPlaceholder
		}
		out["oauth"] = copied
	}
	return out
}

// restoreMCPSecrets 把提交文档里的 *** 还原成磁盘上的原值，并拒绝新增/修改命令型凭据。
func restoreMCPSecrets(want, original map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(want))
	for key, value := range want {
		out[key] = value
	}
	wantServers, ok := want["mcpServers"].(map[string]any)
	if !ok {
		if want["mcpServers"] != nil {
			return nil, protocol.E("invalid_params", "mcpServers 必须是对象")
		}
		return out, nil
	}
	origServers, _ := original["mcpServers"].(map[string]any)
	merged := make(map[string]any, len(wantServers))
	for name, value := range wantServers {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, protocol.E("invalid_params", "服务器条目必须是对象")
		}
		orig, _ := origServers[name].(map[string]any)
		restored, err := restoreMCPServerSecrets(entry, orig)
		if err != nil {
			return nil, err
		}
		merged[name] = restored
	}
	out["mcpServers"] = merged
	return out, nil
}

func restoreMCPServerSecrets(want, original map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(want))
	for key, value := range want {
		out[key] = value
	}
	for _, field := range []string{"env", "headers"} {
		wantMap, ok := want[field].(map[string]any)
		if !ok {
			continue
		}
		origMap, _ := original[field].(map[string]any)
		merged := make(map[string]any, len(wantMap))
		for key, value := range wantMap {
			text, ok := value.(string)
			if !ok {
				return nil, protocol.E("invalid_params", field+" 的值必须是字符串")
			}
			prior, _ := origMap[key].(string)
			if text == secretPlaceholder {
				if prior == "" {
					return nil, protocol.E("invalid_params", "秘密占位符无法匹配原值，请明确填写或移除该字段")
				}
				text = prior
			}
			// env/headers 是 Pi 会解析 !command 的字段：网页只能保留本机已有表达式。
			if strings.HasPrefix(text, "!") && prior != text {
				return nil, protocol.E("invalid_params", "网页不能新增或修改命令型凭据，只能保留本机已有表达式")
			}
			merged[key] = text
		}
		out[field] = merged
	}
	if oauth, ok := want["oauth"].(map[string]any); ok {
		copied := make(map[string]any, len(oauth))
		for key, value := range oauth {
			copied[key] = value
		}
		if value, exists := copied["clientSecret"]; exists {
			text, ok := value.(string)
			if !ok {
				return nil, protocol.E("invalid_params", "oauth.clientSecret 必须是字符串")
			}
			if text == secretPlaceholder {
				origOAuth, _ := original["oauth"].(map[string]any)
				prior, _ := origOAuth["clientSecret"].(string)
				if prior == "" {
					return nil, protocol.E("invalid_params", "秘密占位符无法匹配原值，请明确填写或移除该字段")
				}
				copied["clientSecret"] = prior
			}
		}
		out["oauth"] = copied
	}
	return out, nil
}

// validateMCPDocument 校验 Pi 的结构与秘密相关字段，不复制完整 MCP schema。
func (c *Config) validateMCPDocument(doc map[string]any) error {
	if value, exists := doc["autoEnableCodemode"]; exists {
		if _, ok := value.(bool); !ok {
			return protocol.E("invalid_params", "autoEnableCodemode 必须是布尔值")
		}
	}
	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		if doc["mcpServers"] != nil {
			return protocol.E("invalid_params", "mcpServers 必须是对象")
		}
		return nil
	}
	if len(servers) > maxMCPServers {
		return protocol.E("limit_exceeded", "服务器数量超过上限")
	}
	for name, value := range servers {
		entry, ok := value.(map[string]any)
		if !ok {
			return protocol.E("invalid_params", "服务器条目必须是对象")
		}
		if msg := validateMCPServer(name, entry); msg != "" {
			return protocol.E("invalid_params", msg)
		}
	}
	return nil
}

// validateMCPServer 返回空串表示合法，否则返回可读的中文原因。
func validateMCPServer(name string, entry map[string]any) string {
	if name == "" || len(name) > mcpNameMaxLen {
		return "服务器名称长度必须在 1 到 128 之间"
	}
	if !mcpNamePattern.MatchString(name) {
		return "服务器名称只能包含字母、数字、下划线与连字符"
	}
	command, hasCommand := entry["command"].(string)
	url, hasURL := entry["url"].(string)
	if !hasCommand && !hasURL {
		return "服务器必须提供 command（stdio）或 url（http）"
	}
	if hasCommand && command == "" {
		return "command 不能为空"
	}
	if hasURL && url == "" {
		return "url 不能为空"
	}
	if value, exists := entry["type"]; exists {
		text, ok := value.(string)
		if !ok || (text != "stdio" && text != "http" && text != "streamable-http") {
			return "type 必须是 stdio、http 或 streamable-http"
		}
	}
	if value, exists := entry["enabled"]; exists {
		if _, ok := value.(bool); !ok {
			return "enabled 必须是布尔值"
		}
	}
	if value, exists := entry["description"]; exists {
		if _, ok := value.(string); !ok {
			return "description 必须是字符串"
		}
	}
	if value, exists := entry["cwd"]; exists {
		if _, ok := value.(string); !ok {
			return "cwd 必须是字符串"
		}
	}
	if value, exists := entry["exposure"]; exists {
		text, ok := value.(string)
		if !ok || !mcpExposures[text] {
			return "exposure 必须是 codemode、deferred、direct 或 hidden"
		}
	}
	if value, exists := entry["timeout"]; exists {
		n, ok := value.(float64)
		if !ok || n <= 0 || math.Trunc(n) != n {
			return "timeout 必须是正整数秒"
		}
	}
	if value, exists := entry["args"]; exists {
		list, ok := value.([]any)
		if !ok {
			return "args 必须是数组"
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return "args 的每一项必须是字符串"
			}
		}
	}
	for _, field := range []string{"env", "headers"} {
		if value, exists := entry[field]; exists {
			m, ok := value.(map[string]any)
			if !ok {
				return field + " 必须是对象"
			}
			for key, item := range m {
				if key == "" {
					return field + " 的名称不能为空"
				}
				if _, ok := item.(string); !ok {
					return field + " 的值必须是字符串"
				}
			}
		}
	}
	if value, exists := entry["toolExposure"]; exists {
		m, ok := value.(map[string]any)
		if !ok {
			return "toolExposure 必须是对象"
		}
		for _, item := range m {
			text, ok := item.(string)
			if !ok || !mcpExposures[text] {
				return "toolExposure 的值必须是 codemode、deferred、direct 或 hidden"
			}
		}
	}
	if value, exists := entry["oauth"]; exists {
		if _, ok := value.(map[string]any); !ok {
			return "oauth 必须是对象"
		}
	}
	return ""
}

func mcpServerInfo(name string, entry map[string]any) MCPServerInfo {
	info := MCPServerInfo{
		Name:      name,
		Transport: describeMCPTransport(entry),
		Enabled:   true,
		Exposure:  "codemode",
		Scope:     "global",
		Source:    "mcp.json",
	}
	if enabled, ok := entry["enabled"].(bool); ok {
		info.Enabled = enabled
	}
	if exposure, ok := entry["exposure"].(string); ok && exposure != "" {
		info.Exposure = exposure
	}
	if description, ok := entry["description"].(string); ok {
		info.Description = description
	}
	if raw, ok := entry["toolExposure"].(map[string]any); ok {
		overrides := make(map[string]string, len(raw))
		for key, value := range raw {
			if text, ok := value.(string); ok {
				overrides[key] = text
			}
		}
		if len(overrides) > 0 {
			info.ToolExposure = overrides
		}
	}
	return info
}

// describeMCPTransport 复刻 Pi 的展示：http 给 URL，stdio 给「命令 + 参数」。
func describeMCPTransport(entry map[string]any) string {
	if url, ok := entry["url"].(string); ok && url != "" {
		return url
	}
	command, _ := entry["command"].(string)
	parts := []string{command}
	if args, ok := entry["args"].([]any); ok {
		for _, item := range args {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, " ")
}

// mcpSecretValues 收集当前配置里的秘密字面值，用于探测输出的二次脱敏。
func (c *Config) mcpSecretValues() []string {
	doc, err := c.readObject("mcp.json")
	if err != nil {
		return nil
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	var out []string
	for _, value := range servers {
		entry, _ := value.(map[string]any)
		for _, field := range []string{"env", "headers"} {
			if m, ok := entry[field].(map[string]any); ok {
				for _, item := range m {
					if text, ok := item.(string); ok && len(text) >= 4 {
						out = append(out, text)
					}
				}
			}
		}
		if oauth, ok := entry["oauth"].(map[string]any); ok {
			if text, ok := oauth["clientSecret"].(string); ok && len(text) >= 4 {
				out = append(out, text)
			}
		}
	}
	return out
}

func redactKnownSecrets(text string, secrets []string) string {
	if text == "" {
		return text
	}
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, secretPlaceholder)
	}
	return text
}

func firstLine(text string) string {
	if at := strings.IndexByte(text, '\n'); at >= 0 {
		return text[:at]
	}
	return text
}
