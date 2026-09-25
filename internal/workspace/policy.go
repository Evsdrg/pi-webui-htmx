// Package workspace 负责把外部路径限制在显式授权的目录内。
package workspace

import (
	"os"
	"path/filepath"
	"pi-bridge-go/internal/protocol"
	"strings"
)

// Policy 是允许的工作区根集合。
type Policy struct{ roots []string }

// New 校验工作区根：必须是目录，且解析符号链接后按真实路径参与后续比对。
func New(roots []string) (*Policy, error) {
	p := &Policy{}
	if len(roots) == 0 {
		return nil, protocol.E("invalid_params", "至少需要一个工作区根目录")
	}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, protocol.E("invalid_params", "工作区根目录不是目录")
		}
		p.roots = append(p.roots, real)
	}
	return p, nil
}

// Directory 校验并返回规范化的目录；越界或不存在一律拒绝。
// 这里只约束桥自己接受的工作区，不构成对 Pi 工具的沙箱。
func (p *Policy) Directory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", protocol.E("invalid_params", "cwd 必须是绝对路径")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", protocol.E("invalid_params", "cwd 不存在")
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", protocol.E("invalid_params", "cwd 必须是目录")
	}
	for _, root := range p.roots {
		rel, err := filepath.Rel(root, real)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", protocol.E("forbidden", "cwd 不在已配置的工作区根内")
}
