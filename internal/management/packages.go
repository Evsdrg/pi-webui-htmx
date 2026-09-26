package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"pi-bridge-go/internal/protocol"
)

// PackageKind 是受管资源的类别。
type PackageKind string

const (
	KindPlugin PackageKind = "plugin"
	KindSkill  PackageKind = "skill"
)

// PackageInfo 是一个已安装资源的状态。
// 刻意不含安装/更新操作：那等于任意代码执行。
type PackageInfo struct {
	Source    string `json:"source"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Version   string `json:"version,omitempty"`
	Latest    string `json:"latest,omitempty"`
	HasUpdate bool   `json:"hasUpdate"`
	Disabled  bool   `json:"disabled"`
	Location  string `json:"location,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Packages 返回已安装的插件与技能清单，并标注是否有新版本。
// 版本查询走 npm registry，失败只影响单个条目的 latest 字段，
// 不讓整个列表失败。
func (c *Config) Packages(ctx context.Context, limits DiscoveryLimits) ([]PackageInfo, error) {
	// 直接读原始文档：Settings() 会把 packages 归一成 []string，
	// 丢掉 disabled 等字段，这里需要原始形态。
	raw, err := c.read("settings.json")
	if err != nil {
		if os.IsNotExist(err) {
			return []PackageInfo{}, nil
		}
		return nil, protocol.E("pi_error", "无法读取 settings.json")
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return nil, protocol.E("invalid_history", "settings.json 不是合法 JSON")
	}
	out := []PackageInfo{}
	seen := map[string]struct{}{}

	// settings.json 的 packages 字段（字符串或 {source,disabled}）。
	if list, ok := doc["packages"].([]any); ok {
		for _, item := range list {
			switch v := item.(type) {
			case string:
				out = appendPackage(out, seen, v, "global", false)
			case map[string]any:
				source, _ := v["source"].(string)
				disabled := false
				if d, ok := v["disabled"].(bool); ok {
					disabled = d
				}
				out = appendPackage(out, seen, source, "global", disabled)
			}
		}
	}
	// 项目级 .pi/packages 之类的配置不在桥的可见范围内，只报全局范围。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Source < out[j].Source
	})

	if limits.Timeout <= 0 {
		limits = DefaultDiscoveryLimits()
	}
	// registry 查询并发执行：一个供应商不可达不应让整个列表等到超时。
	// 整体受同一个 ctx 约束，任一查询失败只影响该条目。
	queryCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	var wg sync.WaitGroup
	for i := range out {
		// 只对 npm 来源查版本；本地路径与 git 来源没有可比对的 registry 版本。
		if !strings.HasPrefix(out[i].Source, "npm:") {
			continue
		}
		name := strings.TrimPrefix(out[i].Source, "npm:")
		if name == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			out[idx].Version = c.installedVersion(name)
			latest, err := c.npmLatest(queryCtx, name, limits)
			if err != nil {
				out[idx].Error = "查询最新版本失败"
				return
			}
			out[idx].Latest = latest
			if out[idx].Version != "" && latest != "" && out[idx].Version != latest {
				out[idx].HasUpdate = true
			}
		}(i, name)
	}
	wg.Wait()
	return out, nil
}

func appendPackage(out []PackageInfo, seen map[string]struct{}, source, scope string, disabled bool) []PackageInfo {
	if source == "" {
		return out
	}
	key := scope + "|" + source
	if _, dup := seen[key]; dup {
		return out
	}
	seen[key] = struct{}{}
	out = append(out, PackageInfo{Source: source, Name: packageDisplayName(source), Scope: scope, Disabled: disabled})
	return out
}

// packageDisplayName 从 npm:<name>@<version> 形式里取可读名称。
func packageDisplayName(source string) string {
	name := strings.TrimPrefix(source, "npm:")
	if i := strings.LastIndex(name, "@"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return source
	}
	return name
}

// installedVersion 从 node_modules 的 package.json 读取已安装版本。
func (c *Config) installedVersion(name string) string {
	if strings.ContainsAny(name, "/\\") && !strings.HasPrefix(name, "@") {
		// 作用域包允许一个斜杠，其余形式拒绝。
		if strings.Count(name, "/") > 1 {
			return ""
		}
	}
	path := filepath.Join(c.agentDir, "node_modules", name, "package.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// npmLatest 查询 npm registry 上的最新版本。
func (c *Config) npmLatest(ctx context.Context, name string, limits DiscoveryLimits) (string, error) {
	if limits.MaxBytes <= 0 {
		limits = DefaultDiscoveryLimits()
	}
	url := "https://registry.npmjs.org/" + strings.TrimPrefix(name, "/") + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", protocol.E("pi_error", "registry 返回非 200")
	}
	var doc struct {
		Version string `json:"version"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&doc); err != nil {
		return "", err
	}
	return doc.Version, nil
}
