// Package management 提供 Pi 配置的受控视图与写入。
// 刻意不提供远程安装/卸载包的能力：那等于任意代码执行。
package management

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"pi-bridge-go/internal/protocol"
)

// Limits 约束配置体积与模型总数。
type Limits struct {
	MaxFileBytes int64
	MaxModels    int
}

func DefaultLimits() Limits { return Limits{8 << 20, 512} }

// Config 管理受控配置；写锁只约束本实例，不能约束外部 CLI。
type Config struct {
	agentDir     string
	limits       Limits
	writeMu      sync.Mutex
	packageSlots chan struct{}
	httpClient   *http.Client
	// registryBaseURL 供测试注入本地 registry；生产固定为 npmjs。
	registryBaseURL string
	// catalogURL 是模型目录（models.dev）的来源，可用 SetCatalogURL 覆盖。
	catalogURL string
}

const defaultRegistryBaseURL = "https://registry.npmjs.org"

// defaultCatalogURL 是模型目录的默认来源。
const defaultCatalogURL = "https://models.dev/api.json"

func NewConfig(agentDir string, limits Limits) *Config {
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = DefaultLimits().MaxFileBytes
	}
	if limits.MaxModels <= 0 {
		limits.MaxModels = DefaultLimits().MaxModels
	}
	return &Config{agentDir: agentDir, limits: limits, packageSlots: make(chan struct{}, maxPackageQueries), httpClient: providerHTTPClient, registryBaseURL: defaultRegistryBaseURL, catalogURL: defaultCatalogURL}
}

// AgentDir 返回受管 Pi 配置目录。供运行时代码（如列扩展文件）与测试定位。
func (c *Config) AgentDir() string { return c.agentDir }

// Raw 返回可编辑的脱敏文档。v1 的 *** 只能表示保留已存在的秘密。
func (c *Config) Raw() (map[string]any, error) {
	doc, err := c.readObject("models.json")
	if os.IsNotExist(err) {
		return map[string]any{"providers": map[string]any{}}, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := redactObject(doc)
	if err != nil {
		return nil, err
	}
	if _, ok := out["providers"]; !ok {
		out["providers"] = map[string]any{}
	}
	return out, nil
}

// ModelsReply 是 config.models 的回执。
//
// Providers 是脱敏后的用户文档子树，形状由用户的 models.json 决定，
// 因此保持 map[string]any——这里固定下来的只是外层信封。
// 用户文档透传（Raw / Settings / Trust）同理，不做事具名类型。
type ModelsReply struct {
	Providers  map[string]any `json:"providers"`
	ModelCount int            `json:"modelCount"`
}

// Models 返回有界模型摘要，ModelCount 表示截断前的模型总数。
func (c *Config) Models() (ModelsReply, error) {
	doc, err := c.Raw()
	if err != nil {
		return ModelsReply{}, err
	}
	providers, ok := doc["providers"].(map[string]any)
	if !ok {
		return ModelsReply{}, protocol.E("invalid_history", "providers 必须是对象")
	}
	total, remaining := 0, c.limits.MaxModels
	for _, name := range sortedKeys(providers) {
		entry, ok := providers[name].(map[string]any)
		if !ok {
			return ModelsReply{}, protocol.E("invalid_history", "provider 必须是对象")
		}
		value, exists := entry["models"]
		if !exists {
			continue
		}
		models, ok := value.([]any)
		if !ok {
			return ModelsReply{}, protocol.E("invalid_history", "models 必须是数组")
		}
		total += len(models)
		count := min(len(models), remaining)
		if count != len(models) {
			entry["models"] = models[:count]
			entry["truncated"] = true
		}
		remaining -= count
	}
	return ModelsReply{Providers: providers, ModelCount: total}, nil
}

func (c *Config) Settings() (map[string]any, error) {
	doc, err := c.readObject("settings.json")
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := redactObject(doc)
	if err != nil {
		return nil, err
	}
	if packages, ok := out["packages"].([]any); ok {
		names := make([]string, 0, len(packages))
		for _, item := range packages {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		out["packages"] = names
	}
	return out, nil
}

func (c *Config) Trust() (map[string]any, error) {
	doc, err := c.readObject("trust.json")
	if os.IsNotExist(err) {
		return map[string]any{"projects": map[string]any{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return redactObject(doc)
}

func (c *Config) readObject(name string) (map[string]any, error) {
	raw, err := c.read(name)
	if err != nil {
		return nil, configReadError(err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, protocol.E("invalid_history", name+" 必须是 JSON 对象")
	}
	return doc, nil
}

func configReadError(err error) error {
	var e *protocol.Error
	if os.IsNotExist(err) || errors.As(err, &e) {
		return err
	}
	return protocol.E("pi_error", "读取配置文件失败")
}

// read 既检查普通文件，也限制实际 reader；Stat 后增长不能绕过字节预算。
func (c *Config) read(name string) ([]byte, error) {
	return readConfigFile(filepath.Join(c.agentDir, name), c.limits.MaxFileBytes)
}

func readConfigFile(path string, maxBytes int64) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, protocol.E("forbidden", "配置不是普通文件")
	}
	if st.Size() > maxBytes {
		return nil, protocol.E("limit_exceeded", "配置文件超过体积上限")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, protocol.E("forbidden", "配置不是普通文件")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, protocol.E("limit_exceeded", "配置文件超过体积上限")
	}
	return body, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// WriteModels 校验副本、按稳定身份保留秘密，再可靠替换文件。
// 并发编辑的 revision/CAS 属于 v2；这里不承诺检测外部非合作写入。
func (c *Config) WriteModels(doc map[string]any) error {
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
	if err := c.validateModelsDocument(want); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	original, err := c.readObject("models.json")
	if err != nil && !os.IsNotExist(err) {
		var e *protocol.Error
		if !errors.As(err, &e) || e.Code != "invalid_history" {
			return err
		}
		// 可用明确的新值修复坏文档，但不能猜测其中的秘密或命令。
		original = nil
	}
	mapped, err := mapConfig(want, original, configObject, false, 0)
	if err != nil {
		return err
	}
	if err := c.validateModelsDocument(mapped.(map[string]any)); err != nil {
		return err
	}
	body, err := json.MarshalIndent(mapped, "", "  ")
	if err != nil {
		return protocol.Wrap("invalid_params", "配置无法序列化", err)
	}
	body = append(body, '\n')
	if int64(len(body)) > c.limits.MaxFileBytes {
		return protocol.E("limit_exceeded", "配置超过体积上限")
	}
	return c.replaceModels(body)
}

func (c *Config) replaceModels(body []byte) error {
	file, err := os.CreateTemp(c.agentDir, ".models-*.tmp")
	if err != nil {
		return protocol.E("pi_error", "创建配置临时文件失败")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return protocol.E("pi_error", "写入配置临时文件失败")
	}
	if err := file.Sync(); err != nil {
		return protocol.E("pi_error", "同步配置临时文件失败")
	}
	if err := file.Close(); err != nil {
		return protocol.E("pi_error", "关闭配置临时文件失败")
	}
	if err := os.Rename(file.Name(), filepath.Join(c.agentDir, "models.json")); err != nil {
		return protocol.E("pi_error", "替换配置文件失败")
	}
	dir, err := os.Open(c.agentDir)
	if err != nil {
		return protocol.E("outcome_unknown", "配置已替换，但无法确认目录同步")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return protocol.E("outcome_unknown", "配置已替换，但无法确认目录同步")
	}
	return nil
}

// validateModelsDocument 校验 Pi 的结构和秘密相关字段，不复制完整模型能力 schema。
func (c *Config) validateModelsDocument(doc map[string]any) error {
	providers, ok := doc["providers"].(map[string]any)
	if !ok {
		return protocol.E("invalid_params", "providers 必须是对象")
	}
	if len(providers) > c.limits.MaxModels {
		return protocol.E("limit_exceeded", "provider 数量超过上限")
	}
	total := 0
	for name, value := range providers {
		if name == "" || len(name) > 128 {
			return protocol.E("invalid_params", "provider 名称长度必须在 1 到 128 之间")
		}
		entry, ok := value.(map[string]any)
		if !ok {
			return protocol.E("invalid_params", "provider 必须是对象")
		}
		if err := validateConfigFields(entry); err != nil {
			return err
		}
		if value, exists := entry["models"]; exists {
			list, ok := value.([]any)
			if !ok {
				return protocol.E("invalid_params", "models 必须是数组")
			}
			total += len(list)
			seen := make(map[string]bool)
			for _, item := range list {
				model, ok := item.(map[string]any)
				if !ok {
					return protocol.E("invalid_params", "模型条目必须是对象")
				}
				id, _ := model["id"].(string)
				if id == "" || len(id) > 128 {
					return protocol.E("invalid_params", "模型 id 长度必须在 1 到 128 之间")
				}
				if seen[id] {
					return protocol.E("invalid_params", "模型 ID 重复，无法确定秘密归属")
				}
				seen[id] = true
				if err := validateConfigFields(model); err != nil {
					return err
				}
			}
		}
		if value, exists := entry["modelOverrides"]; exists {
			overrides, ok := value.(map[string]any)
			if !ok {
				return protocol.E("invalid_params", "modelOverrides 必须是对象")
			}
			total += len(overrides)
			for _, item := range overrides {
				model, ok := item.(map[string]any)
				if !ok {
					return protocol.E("invalid_params", "modelOverrides 条目必须是对象")
				}
				if err := validateConfigFields(model); err != nil {
					return err
				}
			}
		}
		if total > c.limits.MaxModels {
			return protocol.E("limit_exceeded", "模型及覆盖项总数超过上限")
		}
	}
	return nil
}

func validateConfigFields(entry map[string]any) error {
	for _, key := range []string{"contextWindow", "maxTokens"} {
		if value, exists := entry[key]; exists {
			n, ok := value.(float64)
			if !ok || n <= 0 || n > 9007199254740991 || math.Trunc(n) != n {
				return protocol.E("invalid_params", key+" 必须为安全范围内的正整数")
			}
		}
	}
	for _, key := range []string{"api", "apiKey"} {
		if value, exists := entry[key]; exists {
			if text, ok := value.(string); !ok || text == "" {
				return protocol.E("invalid_params", key+" 必须是非空字符串")
			}
		}
	}
	if value, exists := entry["headers"]; exists {
		headers, ok := value.(map[string]any)
		if !ok {
			return protocol.E("invalid_params", "headers 必须是对象")
		}
		if len(headers) > 128 {
			return protocol.E("limit_exceeded", "自定义头部数量超过上限")
		}
		seen := make(map[string]bool)
		for key, value := range headers {
			if !validHeaderName(key) {
				return protocol.E("invalid_params", "自定义头部名称无效")
			}
			if _, ok := value.(string); !ok {
				return protocol.E("invalid_params", "自定义头部值必须是字符串")
			}
			lower := strings.ToLower(key)
			if seen[lower] {
				return protocol.E("invalid_params", "头部名称存在大小写冲突")
			}
			seen[lower] = true
		}
	}
	return nil
}

// SetCatalogURL 覆盖模型目录来源，供内网镜像与测试使用。
//
// 只由部署者与测试设置，**不接受网页输入**：目录 URL 会成为一条出站请求，
// 允许网页指定目标就等于给它一条 SSRF 通道。空值与非法地址会被忽略，
// 保持默认来源而不是让配置把自己关掉。
func (c *Config) SetCatalogURL(target string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return
	}
	c.catalogURL = target
}
