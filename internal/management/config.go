// Package management 提供对 Pi 自身配置的只读视图。
// 刻意不提供远程安装/卸载包的能力：那等于任意代码执行。
package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pi-bridge-go/internal/protocol"
)

// secretKeys 是被认为是密钥的字段名，读取时一律打码。
var secretKeys = map[string]struct{}{
	"apikey": {}, "api_key": {}, "token": {}, "secret": {}, "password": {},
	"authorization": {}, "auth": {}, "credential": {}, "credentials": {},
}

// Limits 约束配置读取的体积。
type Limits struct {
	MaxFileBytes int64
	MaxModels    int
}

// DefaultLimits 给出默认限额。
func DefaultLimits() Limits { return Limits{MaxFileBytes: 8 << 20, MaxModels: 512} }

// Config 是 Pi 配置的只读视图。
type Config struct {
	agentDir string
	limits   Limits
}

// NewConfig 构造配置视图；agentDir 为 Pi 配置目录。
func NewConfig(agentDir string, limits Limits) *Config {
	return &Config{
		agentDir: agentDir,
		limits:   limits,
	}
}

// Raw 返回 models.json 的原始文档，密钥字段被打码，供前端编辑使用。
func (c *Config) Raw() (map[string]any, error) {
	raw, err := c.read("models.json")
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"providers": map[string]any{}}, nil
		}
		return nil, protocol.E("pi_error", "无法读取 models.json")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, protocol.E("invalid_history", "models.json 不是合法 JSON")
	}
	out, _ := redact(doc).(map[string]any)
	if _, ok := out["providers"]; !ok {
		out["providers"] = map[string]any{}
	}
	return out, nil
}

// Models 返回 models.json 中的自定义 provider 与模型，密钥字段被打码。
func (c *Config) Models() (map[string]any, error) {
	raw, err := c.read("models.json")
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"providers": map[string]any{}}, nil
		}
		return nil, protocol.E("pi_error", "无法读取 models.json")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, protocol.E("invalid_history", "models.json 不是合法 JSON")
	}
	redacted, _ := redact(doc).(map[string]any)
	providers, _ := redacted["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	// 限制模型数量，避免异常配置造成过大响应。
	total := 0
	for name, v := range providers {
		entry, ok := v.(map[string]any)
		if !ok {
			continue
		}
		models, _ := entry["models"].(map[string]any)
		if len(models) > c.limits.MaxModels {
			trimmed := map[string]any{}
			for i, k := range sortedKeys(models) {
				if i >= c.limits.MaxModels {
					break
				}
				trimmed[k] = models[k]
			}
			entry["models"] = trimmed
			entry["truncated"] = true
			providers[name] = entry
		}
		total += len(models)
	}
	return map[string]any{"providers": providers, "modelCount": total}, nil
}

// Settings 返回 settings.json 的摘要视图，密钥字段被打码。
func (c *Config) Settings() (map[string]any, error) {
	raw, err := c.read("settings.json")
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, protocol.E("pi_error", "无法读取 settings.json")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, protocol.E("invalid_history", "settings.json 不是合法 JSON")
	}
	out, _ := redact(doc).(map[string]any)
	// packages 只给数量与名称，避免响应过大。
	if packages, ok := out["packages"].([]any); ok {
		names := make([]string, 0, len(packages))
		for _, p := range packages {
			if s, ok := p.(string); ok {
				names = append(names, s)
			}
		}
		out["packages"] = names
	}
	return out, nil
}

// Trust 返回项目信任决定，只读。
func (c *Config) Trust() (map[string]any, error) {
	raw, err := c.read("trust.json")
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"projects": map[string]any{}}, nil
		}
		return nil, protocol.E("pi_error", "无法读取 trust.json")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, protocol.E("invalid_history", "trust.json 不是合法 JSON")
	}
	out, _ := redact(doc).(map[string]any)
	return out, nil
}

// read 读取配置文件。刻意不做内容缓存：配置可能被本地 CLI 随时改写，
// 读盘成本受 MaxFileBytes 约束，正确性优先于省一次 stat。
func (c *Config) read(name string) ([]byte, error) {
	path := filepath.Join(c.agentDir, name)
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, protocol.E("forbidden", "配置不是普通文件")
	}
	if st.Size() > c.limits.MaxFileBytes {
		return nil, protocol.E("limit_exceeded", "配置文件超过体积上限")
	}
	return os.ReadFile(path)
}

// redact 递归打码密钥字段，避免把凭据发到浏览器。
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if _, secret := secretKeys[strings.ToLower(k)]; secret {
				out[k] = "***"
				continue
			}
			out[k] = redact(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redact(item)
		}
		return out
	default:
		return v
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WriteModels 原子写入 models.json。
// 设计约束：
//   - 先序列化到同目录临时文件，再 rename，避免半截配置被 Pi 读到；
//   - 落盘前做结构校验，拒绝把明显损坏的文档写进去；
//   - 密钥字段原样保留（前端编辑时用占位符回传的会保持原值）。
func (c *Config) WriteModels(doc map[string]any) error {
	if doc == nil {
		return protocol.E("invalid_params", "配置不能为空")
	}
	providers, ok := doc["providers"]
	if !ok {
		return protocol.E("invalid_params", "配置必须包含 providers 字段")
	}
	if _, ok := providers.(map[string]any); !ok {
		return protocol.E("invalid_params", "providers 必须是对象")
	}
	if err := c.validateModelsDocument(doc); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return protocol.E("pi_error", "配置序列化失败")
	}
	b = append(b, '\n')
	if int64(len(b)) > c.limits.MaxFileBytes {
		return protocol.E("limit_exceeded", "配置超过体积上限")
	}
	path := filepath.Join(c.agentDir, "models.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return protocol.E("pi_error", "写入临时文件失败")
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return protocol.E("pi_error", "替换配置文件失败")
	}
	return nil
}

// validateModelsDocument 在落盘前拦截明显损坏的配置。
func (c *Config) validateModelsDocument(doc map[string]any) error {
	providers, _ := doc["providers"].(map[string]any)
	if len(providers) > c.limits.MaxModels {
		return protocol.E("limit_exceeded", "provider 数量超过上限")
	}
	for name, v := range providers {
		if name == "" || len(name) > 128 {
			return protocol.E("invalid_params", "provider 名称长度必须在 1 到 128 之间")
		}
		entry, ok := v.(map[string]any)
		if !ok {
			return protocol.E("invalid_params", "provider "+name+" 必须是对象")
		}
		if models, exists := entry["models"]; exists {
			list, ok := models.(map[string]any)
			if !ok {
				return protocol.E("invalid_params", "provider "+name+" 的 models 必须是对象")
			}
			if len(list) > c.limits.MaxModels {
				return protocol.E("limit_exceeded", "provider "+name+" 的模型数量超过上限")
			}
			for id := range list {
				if id == "" || len(id) > 128 {
					return protocol.E("invalid_params", "模型 ID 长度必须在 1 到 128 之间")
				}
			}
		}
		if api, exists := entry["api"]; exists {
			s, ok := api.(string)
			if !ok || (!strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://")) {
				return protocol.E("invalid_params", "provider "+name+" 的 api 必须是 http(s) 地址")
			}
		}
	}
	return nil
}
