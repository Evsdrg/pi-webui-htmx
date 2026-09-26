package presentation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"pi-bridge-go/internal/protocol"
	"strings"
)

type viteEntry struct {
	File    string   `json:"file"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
	IsEntry bool     `json:"isEntry"`
}

// loadManifest 校验模板契约，并沿 Vite 静态依赖图加载首屏 CSS。
// 不按 app-* 猜文件名，避免旧构建残留被当作第二个入口执行。
func (r *Renderer) loadManifest(dir string, supported []string) error {
	var ui struct {
		ProtocolVersion int               `json:"protocolVersion"`
		RequiredMethods []string          `json:"requiredMethods"`
		Templates       map[string]string `json:"templates"`
		Build           struct {
			Entry string `json:"entry"`
		} `json:"build"`
	}
	read := func(path string, out any) error {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return protocol.E("invalid_params", "UI manifest 缺失或超过体积上限")
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, out) != nil {
			return protocol.E("invalid_params", "UI manifest 不是合法 JSON")
		}
		return nil
	}
	if err := read(filepath.Join(dir, "ui-manifest.json"), &ui); err != nil {
		return err
	}
	if ui.ProtocolVersion != protocol.Version {
		return protocol.E("unsupported_version", "UI 协议版本不兼容")
	}
	if len(supported) > 0 {
		methods := map[string]bool{}
		for _, method := range supported {
			methods[method] = true
		}
		for _, method := range ui.RequiredMethods {
			if !methods[method] {
				return protocol.E("unsupported_method", "UI 要求桥不支持的方法: "+method)
			}
		}
	}
	for key, path := range ui.Templates {
		if strings.HasPrefix(key, "_") {
			continue
		}
		name := strings.TrimPrefix(path, "templates/")
		if name == path || r.templates[name] == nil {
			return protocol.E("not_found", "UI 必需模板缺失: "+key)
		}
	}
	var manifest map[string]viteEntry
	if err := read(filepath.Join(dir, "dist", ".vite", "manifest.json"), &manifest); err != nil {
		return err
	}
	entry, ok := manifest[ui.Build.Entry]
	if !ok || !entry.IsEntry {
		return protocol.E("not_found", "Vite manifest 缺少声明的入口")
	}
	assetName := func(path string) (string, error) {
		name := strings.TrimPrefix(path, "assets/")
		if path == name || strings.ContainsAny(name, "/\\") || r.assets[name].path == "" {
			return "", protocol.E("not_found", "Vite 引用了不存在的资源")
		}
		return name, nil
	}
	js, err := assetName(entry.File)
	if err != nil {
		return err
	}
	r.entryJS = []string{js}
	visited, styles := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visited[key] {
			return nil
		}
		visited[key] = true
		item, ok := manifest[key]
		if !ok {
			return protocol.E("not_found", "Vite 静态依赖缺失")
		}
		if _, err := assetName(item.File); err != nil {
			return err
		}
		for _, dependency := range item.Imports {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		for _, path := range item.CSS {
			name, err := assetName(path)
			if err != nil {
				return err
			}
			if !styles[name] {
				styles[name] = true
				r.entryCSS = append(r.entryCSS, name)
			}
		}
		return nil
	}
	return visit(ui.Build.Entry)
}
