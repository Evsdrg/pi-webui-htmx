// Package childenv 隔离桥的服务凭据与业务子进程环境。
package childenv

import "strings"

// Filter 保留 Pi/API/代理等正常环境，移除桥及 relay 的私有配置。
// 这防止意外继承，不构成对同 UID 恶意进程的操作系统隔离。
func Filter(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "PI_BRIDGE_") || strings.HasPrefix(name, "PI_RELAY_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
