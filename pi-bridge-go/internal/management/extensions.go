package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExtensionFile 是随 Pi 自动加载的扩展示意项（不是 npm 包）。
//
// 为什么要单独列：Pi 启动时会在 <agent-dir>/extensions/ 下按目录约定自动加载
// 扩展文件（*.ts / *.js，或含 index.* / package.json 的子目录）。这类扩展
// 不出现在 settings.json 的 packages 数组里，因此只看 packages 的清单会把
// 「已经在跑的本地扩展」整个漏掉——用户会以为插件没装。
type ExtensionFile struct {
	// Name 是展示名：文件用文件名，目录用目录名。
	Name string
	// Path 是相对 agent 目录的路径（如 extensions/zh-system-prompt.ts）。
	Path string
	// Kind 是来源形态：file（单文件）/ dir（目录入口）。
	Kind string
}

const maxExtensionEntries = 512

// Extensions 列出 <agent-dir>/extensions 下会被 Pi 自动加载的扩展条目。
//
// 判定与 Pi 的 discoverExtensionsInDir 对齐（只下一层，不递归）：
//   - 文件：*.ts 或 *.js
//   - 子目录：含 index.ts / index.js，或含声明了 pi.extensions 的 package.json
//
// 只读取目录结构，不执行、不解析扩展内容——面板只做展示。
func (c *Config) Extensions() ([]ExtensionFile, error) {
	dir := filepath.Join(c.agentDir, "extensions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []ExtensionFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []ExtensionFile{}
	for _, entry := range entries {
		if len(out) >= maxExtensionEntries {
			break
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// ReadDir 对符号链接：Type 不足以判断目标形态，用 Info 跟随。
		switch {
		case info.Mode().IsRegular() && isExtensionFile(name):
			out = append(out, ExtensionFile{Name: name, Path: filepath.Join("extensions", name), Kind: "file"})
		case info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			if _, ok := resolveExtensionEntry(filepath.Join(dir, name)); ok {
				out = append(out, ExtensionFile{Name: name, Path: filepath.Join("extensions", name), Kind: "dir"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func isExtensionFile(name string) bool {
	return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")
}

// resolveExtensionEntry 判断一个子目录是否构成 Pi 可加载的扩展入口。
// 第二个返回值是入口文件路径（仅用于判定成功，不对外暴露）。
func resolveExtensionEntry(dir string) (string, bool) {
	// package.json 里的 pi.extensions 优先（与 Pi 的 readPiManifest 对齐）。
	if raw, err := readConfigFile(filepath.Join(dir, "package.json"), 1<<20); err == nil {
		var manifest struct {
			Pi struct {
				Extensions []string `json:"extensions"`
			} `json:"pi"`
		}
		if json.Unmarshal(raw, &manifest) == nil {
			for _, rel := range manifest.Pi.Extensions {
				path := filepath.Join(dir, filepath.FromSlash(rel))
				if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
					return path, true
				}
			}
		}
	}
	for _, candidate := range []string{"index.ts", "index.js"} {
		path := filepath.Join(dir, candidate)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}
