package management

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/mod/semver"
	"pi-bridge-go/internal/protocol"
)

const (
	maxPackages        = 128
	maxPackageQueries  = 4
	maxPackageMetadata = 1 << 20
)

type PackageKind string

const (
	KindPlugin PackageKind = "plugin"
	KindSkill  PackageKind = "skill"
)

// PackageInfo 描述配置来源及可确认的版本，不提供安装/更新操作。
// Disabled 保留现有配置元数据，不代表运行中 Pi 的资源加载状态。
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

// Packages 的条目数、worker 数与跨请求 HTTP 并发都有上限。
// 单个 registry 失败只标记对应条目，不执行 npmCommand 或启动 Pi。
func (c *Config) Packages(ctx context.Context, limits DiscoveryLimits) ([]PackageInfo, error) {
	doc, err := c.readObject("settings.json")
	if os.IsNotExist(err) {
		return []PackageInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	list, ok := doc["packages"].([]any)
	if !ok && doc["packages"] != nil {
		return nil, protocol.E("invalid_history", "packages 必须是数组")
	}
	if len(list) > maxPackages {
		return nil, protocol.E("limit_exceeded", "配置包数量超过 128 条上限")
	}
	out := []PackageInfo{}
	seen := map[string]struct{}{}
	for _, item := range list {
		var source string
		disabled := false
		switch v := item.(type) {
		case string:
			source = v
		case map[string]any:
			source, _ = v["source"].(string)
			disabled, _ = v["disabled"].(bool)
		}
		if len(source) > 1024 {
			return nil, protocol.E("limit_exceeded", "包来源长度超过上限")
		}
		out = appendPackage(out, seen, source, "global", disabled)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	limits = normalizeDiscoveryLimits(limits)
	queryCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	groups := map[string][]int{}
	for i := range out {
		if !strings.HasPrefix(out[i].Source, "npm:") {
			continue
		}
		name, valid := npmPackageName(out[i].Source)
		if !valid {
			out[i].Error = "npm 来源格式不支持版本查询"
			continue
		}
		groups[name] = append(groups[name], i)
		out[i].Error = "最新版本查询未完成"
	}
	type job struct {
		name    string
		indexes []int
	}
	jobs := make(chan job, len(groups))
	for name, indexes := range groups {
		if queryCtx.Err() != nil {
			break
		}
		version := c.installedVersion(name)
		for _, i := range indexes {
			out[i].Version = version
		}
		jobs <- job{name, indexes}
	}
	close(jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < min(maxPackageQueries, len(groups)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if queryCtx.Err() != nil {
					return
				}
				latest, err := c.queryPackageVersion(queryCtx, task.name, limits)
				for _, i := range task.indexes {
					if err != nil {
						out[i].Error = "查询最新版本失败"
						continue
					}
					out[i].Latest = latest
					out[i].Error = ""
					if out[i].Version == "" {
						out[i].Error = "无法读取 Pi 受管安装版本"
						continue
					}
					if !validVersion(out[i].Version) {
						out[i].Error = "已安装版本无法按语义版本比较"
						continue
					}
					out[i].HasUpdate = semver.Compare(versionKey(latest), versionKey(out[i].Version)) > 0
				}
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, protocol.E("timeout", "包查询已取消")
	}
	return out, nil
}

func (c *Config) queryPackageVersion(ctx context.Context, name string, limits DiscoveryLimits) (string, error) {
	select {
	case c.packageSlots <- struct{}{}:
		defer func() { <-c.packageSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return c.npmLatest(ctx, name, limits)
}

func appendPackage(out []PackageInfo, seen map[string]struct{}, source, scope string, disabled bool) []PackageInfo {
	if source == "" {
		return out
	}
	key := scope + "|" + source
	if _, exists := seen[key]; exists {
		return out
	}
	seen[key] = struct{}{}
	return append(out, PackageInfo{Source: source, Name: packageDisplayName(source), Scope: scope, Disabled: disabled})
}

func packageDisplayName(source string) string {
	if name, ok := npmPackageName(source); ok {
		return name
	}
	return source
}

// npmPackageName 把 pin 与身份分离；不把 alias/file/git 指定方式误当 registry 包。
func npmPackageName(source string) (string, bool) {
	if !strings.HasPrefix(source, "npm:") {
		return "", false
	}
	name := strings.TrimPrefix(source, "npm:")
	start := 0
	if strings.HasPrefix(name, "@") {
		start = 1
	}
	if i := strings.IndexByte(name[start:], '@'); i >= 0 {
		i += start
		version := name[i+1:]
		if strings.ContainsAny(version, "/:\\\r\n") {
			return "", false
		}
		name = name[:i]
	}
	return name, validNpmName(name)
}

func validNpmName(name string) bool {
	if len(name) == 0 || len(name) > 214 {
		return false
	}
	parts := strings.Split(name, "/")
	if strings.HasPrefix(name, "@") {
		if len(parts) != 2 {
			return false
		}
		parts[0] = strings.TrimPrefix(parts[0], "@")
	} else if len(parts) != 1 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
				return false
			}
		}
	}
	return true
}

// Pi 0.85.1 的 getManagedNpmInstallPath 使用 agentDir/npm/node_modules。
// 无法从该位置确认时返回未知，不执行 npmCommand 去猜旧式全局目录。
func (c *Config) installedVersion(name string) string {
	if !validNpmName(name) {
		return ""
	}
	path := filepath.Join(c.agentDir, "npm", "node_modules", filepath.FromSlash(name), "package.json")
	body, err := readConfigFile(path, maxPackageMetadata)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &pkg) != nil || len(pkg.Version) > 128 {
		return ""
	}
	return pkg.Version
}

func (c *Config) npmLatest(ctx context.Context, name string, limits DiscoveryLimits) (string, error) {
	if !validNpmName(name) {
		return "", protocol.E("invalid_params", "npm 包名无效")
	}
	limits = normalizeDiscoveryLimits(limits)
	limits.MaxBytes = min(limits.MaxBytes, maxPackageMetadata)
	target := c.registryBaseURL + "/" + url.PathEscape(name) + "/latest"
	body, err := c.fetchJSON(ctx, target, "", "", map[string]string{"Accept": "application/vnd.npm.install-v1+json"}, limits)
	if err != nil {
		return "", err
	}
	var doc struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &doc) != nil || !validVersion(doc.Version) {
		return "", protocol.E("pi_error", "registry 版本信息无效")
	}
	return doc.Version, nil
}

func versionKey(version string) string { return "v" + strings.TrimPrefix(version, "v") }
func validVersion(version string) bool {
	return len(version) > 0 && len(version) <= 128 && semver.IsValid(versionKey(version))
}
