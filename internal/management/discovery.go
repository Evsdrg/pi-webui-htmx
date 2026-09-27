package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
)

// DiscoveryLimits 约束外部查询的体积与耗时。
type DiscoveryLimits struct {
	Timeout    time.Duration
	MaxBytes   int64
	MaxModels  int
	MaxCatalog int
}

// DefaultDiscoveryLimits 给出默认限额。
func DefaultDiscoveryLimits() DiscoveryLimits {
	return DiscoveryLimits{
		Timeout:    20 * time.Second,
		MaxBytes:   8 << 20,
		MaxModels:  500,
		MaxCatalog: 2000,
	}
}

// providerHTTPClient 不继承 DefaultClient 的 Cookie/重定向策略。
// 自定义鉴权头不受标准库的跨主机 Authorization 剥离规则保护。
var providerHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func normalizeDiscoveryLimits(limits DiscoveryLimits) DiscoveryLimits {
	defaults := DefaultDiscoveryLimits()
	if limits.Timeout <= 0 {
		limits.Timeout = defaults.Timeout
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxModels <= 0 {
		limits.MaxModels = defaults.MaxModels
	}
	if limits.MaxCatalog <= 0 {
		limits.MaxCatalog = defaults.MaxCatalog
	}
	return limits
}

// DiscoveredModel 是供应商 /models 返回的一条模型。
type DiscoveredModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	ContextSize int      `json:"contextSize,omitempty"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Reasoning   bool     `json:"reasoning,omitempty"`
	Input       []string `json:"input,omitempty"`
}

// Discover 向供应商的 /models 端点查询可用模型。
// URL 构造与 Pi Web 的 buildModelsListUrl 保持一致，保证同一供应商行为相同。
func (c *Config) Discover(ctx context.Context, baseURL, api, apiKey string, headers map[string]string, limits DiscoveryLimits) ([]DiscoveredModel, error) {
	limits = normalizeDiscoveryLimits(limits)
	target, err := buildModelsListURL(baseURL, api)
	if err != nil {
		return nil, err
	}
	body, err := c.fetchJSON(ctx, target, api, apiKey, headers, limits)
	if err != nil {
		return nil, err
	}
	return parseDiscoveredModels(body, limits.MaxModels)
}

// TestConnection 用一个最小请求验证供应商凭据是否可用。
// 只发一次极短请求，不产生实质费用。
func (c *Config) TestConnection(ctx context.Context, baseURL, api, apiKey string, headers map[string]string, limits DiscoveryLimits) (map[string]any, error) {
	limits = normalizeDiscoveryLimits(limits)
	target, err := buildModelsListURL(baseURL, api)
	if err != nil {
		return nil, err
	}
	body, err := c.fetchJSON(ctx, target, api, apiKey, headers, limits)
	if err != nil {
		return nil, err
	}
	models, err := parseDiscoveredModels(body, 1)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ok": true, "modelsListed": len(models) > 0}
	if len(models) > 0 {
		out["firstModelId"] = models[0].ID
	}
	return out, nil
}

// Catalog 返回 models.dev 的模型目录，用于「按型号补全参数」。
// 带超时与体积上限；失败不影响本地编辑。
func (c *Config) Catalog(ctx context.Context, limits DiscoveryLimits) ([]DiscoveredModel, error) {
	limits = normalizeDiscoveryLimits(limits)
	body, err := c.fetchJSON(ctx, "https://models.dev/api.json", "", "", nil, limits)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return nil, protocol.E("pi_error", "models.dev 返回的不是合法 JSON")
	}
	out := make([]DiscoveredModel, 0, 64)
	for provider, v := range doc {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		models, _ := entry["models"].(map[string]any)
		for id, mv := range models {
			if len(out) >= limits.MaxCatalog {
				return out, nil
			}
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			item := DiscoveredModel{
				ID:   provider + "/" + id,
				Name: stringField(m, "name"),
			}
			if limit, ok := m["limit"].(map[string]any); ok {
				item.ContextSize = intField(limit, "context")
				item.MaxTokens = intField(limit, "output")
			}
			if reasoning, ok := m["reasoning"].(bool); ok {
				item.Reasoning = reasoning
			}
			if input, ok := m["input"].([]any); ok {
				for _, v := range input {
					if s, ok := v.(string); ok {
						item.Input = append(item.Input, s)
					}
				}
			}
			out = append(out, item)
		}
	}
	return out, nil
}

// fetchJSON 发起带鉴权的 GET 并读取有界响应体。
func (c *Config) fetchJSON(ctx context.Context, target, api, apiKey string, headers map[string]string, limits DiscoveryLimits) ([]byte, error) {
	limits = normalizeDiscoveryLimits(limits)
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return nil, protocol.E("invalid_params", "目标地址不是合法 URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, protocol.E("invalid_params", "只支持 http/https")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, protocol.E("invalid_params", "地址不能包含用户凭据或片段")
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, protocol.E("invalid_params", "构造请求失败")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		// 只接受受控头部名，拒绝注入换行或控制字符。
		if !validHeaderName(k) || !validHeaderValue(v) {
			return nil, protocol.E("invalid_params", "自定义头部包含非法字符")
		}
		req.Header.Set(k, v)
	}
	switch api {
	case "anthropic-messages":
		if req.Header.Get("x-api-key") == "" && apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case "google-generative-ai":
		if req.Header.Get("x-goog-api-key") == "" && apiKey != "" {
			req.Header.Set("x-goog-api-key", apiKey)
		}
	default:
		if req.Header.Get("Authorization") == "" && apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, protocol.E("pi_error", "请求供应商失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limits.MaxBytes+1))
	if err != nil {
		return nil, protocol.E("pi_error", "读取响应失败")
	}
	if int64(len(body)) > limits.MaxBytes {
		return nil, protocol.E("limit_exceeded", "供应商响应超过体积上限")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 上游可能回显凭据；错误正文不进入浏览器提示或桥日志。
		return nil, protocol.E("pi_error", fmt.Sprintf("供应商返回 HTTP %d", resp.StatusCode))
	}
	return body, nil
}

// validHeaderName 拒绝头部名中的非法字符。
func validHeaderName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// validHeaderValue 拒绝头部值中的控制字符，防止请求走私。
func validHeaderValue(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for _, c := range value {
		if c < 0x20 && c != 0x09 {
			return false
		}
		if c == 0x7f {
			return false
		}
	}
	return true
}

// buildModelsListURL 与 Pi Web 的实现保持一致。
func buildModelsListURL(baseURL, api string) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", protocol.E("invalid_params", "baseURL 不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", protocol.E("invalid_params", "baseURL 不是合法 URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", protocol.E("invalid_params", "baseURL 只支持 http/https")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), "/models") {
		if api == "anthropic-messages" && !hasVersionSuffix(path) {
			path += "/v1"
		}
		if api == "google-generative-ai" && !hasVersionSuffix(path) {
			path += "/v1beta"
		}
		path += "/models"
	}
	parsed.Path = strings.ReplaceAll(path, "//", "/")
	query := parsed.Query()
	switch api {
	case "anthropic-messages":
		if query.Get("limit") == "" {
			query.Set("limit", "1000")
		}
	case "google-generative-ai":
		if query.Get("pageSize") == "" {
			query.Set("pageSize", "1000")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// hasVersionSuffix 判断路径是否已带 /v1、/v1beta 之类的版本段。
func hasVersionSuffix(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 {
		return false
	}
	last := segments[len(segments)-1]
	if len(last) < 2 || last[0] != 'v' {
		return false
	}
	for _, c := range last[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseDiscoveredModels 解析多种常见响应形状。
func parseDiscoveredModels(body []byte, maxModels int) ([]DiscoveredModel, error) {
	if maxModels <= 0 {
		maxModels = 500
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return nil, protocol.E("pi_error", "响应不是合法 JSON")
	}
	var items []any
	switch v := doc.(type) {
	case []any:
		items = v
	case map[string]any:
		if data, ok := v["data"].([]any); ok {
			items = data
		} else if models, ok := v["models"].([]any); ok {
			items = models
		} else if models, ok := v["models"].(map[string]any); ok {
			// Google 风格：models 是对象，键即模型 ID。
			for id, mv := range models {
				items = append(items, map[string]any{"id": id, "raw": mv})
			}
		} else {
			return []DiscoveredModel{}, nil
		}
	default:
		return nil, protocol.E("pi_error", "无法识别的响应结构")
	}
	out := make([]DiscoveredModel, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := stringField(m, "id")
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, DiscoveredModel{
			ID:          id,
			Name:        stringField(m, "name"),
			ContextSize: intField(m, "context_size"),
			MaxTokens:   intField(m, "max_tokens"),
			Reasoning:   boolField(m, "reasoning"),
			Input:       stringsField(m, "input"),
		})
		if len(out) >= maxModels {
			break
		}
	}
	return out, nil
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func stringsField(m map[string]any, key string) []string {
	list, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
