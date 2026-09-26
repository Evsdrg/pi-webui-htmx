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
//   - 密钥字段按原文保留：Raw() 会把密钥打码成 "***"，前端编辑后原样
//     回传时必须还原成真实值，否则一次「只改模型名」的保存就会把全部
//     API key 覆写成字面量 "***"。
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
	if err := c.restoreSecrets(doc); err != nil {
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
//
// 校验依据是 Pi 自己的 schema（dist/core/model-config.js 的
// ModelsConfigSchema），不是我们的想象。曾经这里要求 provider.models 是
// 对象、api 必须带 http(s) 前缀——Pi 实际要求 models 是**数组**、api 只是
// 任意非空字符串。结果是一份 Pi 完全接受的配置被桥拒绝，用户无法保存。
//
// 因此这里只检查 Pi 也检查、且我们能稳定判断的部分：
//   - providers 必须是对象，键名非空
//   - models 若存在必须是数组，每项必须有非空 id
//   - 其余字段交给 Pi 校验——桥放宽比误拒更安全，误拒会让合法配置无法保存
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
		// Pi 的 api 只是非空字符串（minLength 1），不要求 http(s)；
		// 收紧到 http(s) 会拒掉 "openai-completions" 这类合法值。
		if api, exists := entry["api"]; exists {
			if text, ok := api.(string); !ok || text == "" {
				return protocol.E("invalid_params", "provider "+name+" 的 api 不能为空字符串")
			}
		}
		if models, exists := entry["models"]; exists {
			// Pi 的 ProviderConfigSchema 里 models 是数组，不是对象。
			list, ok := models.([]any)
			if !ok {
				return protocol.E("invalid_params", "provider "+name+" 的 models 必须是数组")
			}
			if len(list) > c.limits.MaxModels {
				return protocol.E("limit_exceeded", "provider "+name+" 的模型数量超过上限")
			}
			for _, item := range list {
				model, ok := item.(map[string]any)
				if !ok {
					return protocol.E("invalid_params", "provider "+name+" 的模型条目必须是对象")
				}
				id, _ := model["id"].(string)
				if id == "" || len(id) > 128 {
					return protocol.E("invalid_params", "provider "+name+" 的模型 id 长度必须在 1 到 128 之间")
				}
			}
		}
	}
	return nil
}

// restoreSecrets 把文档里值为占位符 \"***\" 的密钥字段还原成磁盘上的真实值。
//
// 为什么必须做：Raw()/Models() 会把密钥打码后发给浏览器，前端编辑完再
// 原样 POST 回来。若不还原，一次「只改模型显示名」的保存就会把该 provider
// 的全部 API key 覆写成字面量 \"***\"，之后所有请求都会带着这个假密钥失败，
// 而界面上看不出任何异常——密钥仍然显示为 \"***\"。
//
// 规则：
//   - 只有值恰好是 \"***\" 且磁盘上同一路径存在非占位真值时，才替换；
//   - 磁盘上没有对应真值（新增 provider、用户真的想写 \"***\"）时保持原样；
//   - 读取磁盘失败不阻塞写入：此时没有任何可还原的值，按用户提交的写。
func (c *Config) restoreSecrets(doc map[string]any) error {
	raw, err := c.read("models.json")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return protocol.E("pi_error", "无法读取 models.json")
	}
	var original map[string]any
	if json.Unmarshal(raw, &original) != nil {
		// 磁盘上已经不是合法 JSON：不猜，按用户提交的写，由校验决定去留。
		return nil
	}
	restoreSecretsInto(doc, original)
	return nil
}

// restoreSecretsInto 递归对齐两棵文档树，只在键名命中 secretKeys 时替换。
func restoreSecretsInto(want, have map[string]any) {
	for key, value := range want {
		if _, secret := secretKeys[strings.ToLower(key)]; !secret {
			// 非密钥字段仍可能嵌套密钥（例如 models 下的 provider 选项）。
			if nested, ok := value.(map[string]any); ok {
				if original, ok := have[key].(map[string]any); ok {
					restoreSecretsInto(nested, original)
				}
			}
			continue
		}
		if text, ok := value.(string); !ok || text != "***" {
			continue
		}
		if original, ok := have[key].(string); ok && original != "" && original != "***" {
			want[key] = original
		}
	}
}
