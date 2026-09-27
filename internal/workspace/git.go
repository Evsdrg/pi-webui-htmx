package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"pi-bridge-go/internal/childenv"
	"pi-bridge-go/internal/protocol"
)

const (
	gitTimeout   = 10 * time.Second
	gitMaxOutput = 2 << 20
	// 状态还会变成 JSON；保留转义和信封空间，不能拿满 WS 的原始字节预算。
	gitStatusBytes = 64 << 10
	gitStderrBytes = 32 << 10
)

var errNotGit = errors.New("不是可用的 Git 仓库")
var errGitOutputLimit = errors.New("Git 输出达到上限")

// gitEnvironment 不接受继承的仓库定位/配置/外部命令注入。
func gitEnvironment() []string {
	out := childenv.Filter(os.Environ())
	clean := make([]string, 0, len(out)+5)
	for _, entry := range out {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			clean = append(clean, entry)
		}
	}
	return append(clean, "LC_ALL=C", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

// runGit 只供本文件及索引中已审过的固定命令使用，不是任意 Git 命令沙箱。
// stdout/stderr 都有限额；达到上限即取消整组，不继续无界丢弃输出。
func (f *Files) runGit(ctx context.Context, dir string, maxBytes int, output io.Writer, args ...string) (bool, int, error) {
	if f.gitPath == "" {
		return false, -1, errNotGit
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fixed := []string{"--no-pager", "--no-lazy-fetch", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}
	cmd := exec.CommandContext(ctx, f.gitPath, append(fixed, args...)...)
	cmd.Dir, cmd.Env = dir, gitEnvironment()
	if err := prepareGitCommand(cmd); err != nil {
		return false, -1, err
	}
	cmd.WaitDelay = 250 * time.Millisecond
	stdout := &gitWriter{dst: output, remaining: maxBytes, cancel: cancel}
	stderr := &gitWriter{dst: io.Discard, remaining: gitStderrBytes, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if parent.Err() != nil {
		return false, -1, protocol.E("timeout", "Git 查询已取消或超时")
	}
	if stderr.truncated {
		return false, -1, protocol.E("limit_exceeded", "Git 诊断输出超过上限")
	}
	if stdout.writeErr != nil {
		return false, -1, protocol.E("pi_error", "处理 Git 输出失败")
	}
	if stdout.truncated {
		return true, 0, nil
	}
	if err == nil {
		return false, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() == 129 {
			return false, -1, protocol.E("pi_error", "Git 不支持受控查询参数，请升级 Git")
		}
		return false, exit.ExitCode(), nil
	}
	return false, -1, protocol.E("pi_error", "运行 Git 失败")
}

type gitWriter struct {
	dst       io.Writer
	remaining int
	cancel    context.CancelFunc
	truncated bool
	writeErr  error
}

func (w *gitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.truncated || w.writeErr != nil {
		return n, nil
	}
	if len(p) > w.remaining {
		p, w.truncated = p[:w.remaining], true
	}
	w.remaining -= len(p)
	_, err := w.dst.Write(p)
	if errors.Is(err, errGitOutputLimit) {
		w.truncated = true
	} else {
		w.writeErr = err
	}
	if w.truncated || w.writeErr != nil {
		w.cancel()
	}
	return n, nil
}

// nulRecords 增量分帧，保留真实文件名；不把全部 Git 输出 Split 成切片。
type nulRecords struct {
	pending []byte
	consume func(string) bool
}

func (w *nulRecords) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			if len(w.pending)+len(p) > 32<<10 {
				return n, errGitOutputLimit
			}
			w.pending = append(w.pending, p...)
			break
		}
		if len(w.pending)+i > 32<<10 {
			return n, errGitOutputLimit
		}
		w.pending = append(w.pending, p[:i]...)
		if !w.consume(string(w.pending)) {
			return n, errGitOutputLimit
		}
		w.pending = w.pending[:0]
		p = p[i+1:]
	}
	return n, nil
}

// gitRepository 检查工作树、Git 元数据与转换配置。授权子目录不等于授权整库。
func (f *Files) gitRepository(ctx context.Context, path string) (string, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	for _, field := range []string{"--show-toplevel", "--absolute-git-dir", "--git-common-dir"} {
		var out bytes.Buffer
		truncated, code, err := f.runGit(ctx, dir, 32<<10, &out, "rev-parse", "--path-format=absolute", field)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", errNotGit
		}
		if truncated {
			return "", protocol.E("limit_exceeded", "Git 元数据路径超过上限")
		}
		if _, _, err := f.resolve(strings.TrimSuffix(out.String(), "\n")); err != nil {
			return "", protocol.E("forbidden", "Git 工作树或元数据位于未授权目录")
		}
	}
	var filters bytes.Buffer
	truncated, code, err := f.runGit(ctx, dir, 32<<10, &filters, "config", "--includes", "--null", "--name-only", "--get-regexp", `^filter\.`)
	if err != nil {
		return "", err
	}
	if truncated || filters.Len() > 0 {
		return "", protocol.E("forbidden", "仓库配置了外部转换过滤器，桥的只读 Git 查询不支持该配置")
	}
	if code != 0 && code != 1 {
		return "", protocol.E("pi_error", "读取 Git 配置失败")
	}
	return dir, nil
}

func gitQueryError(err error) error {
	if errors.Is(err, errNotGit) {
		return protocol.E("not_found", "目标不是可用的 Git 仓库")
	}
	return err
}

// GitStatus 用 NUL 分帧解析状态，同时覆盖空仓库和 detached HEAD。
func (f *Files) GitStatus(ctx context.Context, path string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	dir, err := f.gitRepository(ctx, path)
	if err != nil {
		return nil, gitQueryError(err)
	}
	branch := ""
	files := []map[string]string{}
	var rename map[string]string
	parser := &nulRecords{consume: func(record string) bool {
		if rename != nil {
			rename["from"] = record
			rename = nil
			return true
		}
		if strings.HasPrefix(record, "## ") {
			branch = strings.TrimPrefix(record, "## ")
			branch = strings.TrimPrefix(branch, "No commits yet on ")
			branch = strings.TrimPrefix(branch, "Initial commit on ")
			if strings.HasPrefix(branch, "HEAD (") {
				branch = "HEAD"
			}
			branch = strings.SplitN(branch, "...", 2)[0]
			return true
		}
		if len(record) < 4 {
			return true
		}
		if len(files) >= f.limits.MaxEntries {
			return false
		}
		item := map[string]string{"status": strings.TrimSpace(record[:2]), "path": record[3:]}
		files = append(files, item)
		if strings.ContainsAny(record[:2], "RC") {
			rename = item
		}
		return true
	}}
	truncated, code, err := f.runGit(ctx, dir, gitStatusBytes, parser, "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all", "--ignore-submodules=dirty")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, protocol.E("pi_error", "Git 状态查询失败")
	}
	if rename != nil {
		files = files[:len(files)-1]
		truncated = true
	}
	if branch == "" {
		return nil, protocol.E("limit_exceeded", "Git 分支信息不完整")
	}
	return map[string]any{"branch": branch, "clean": len(files) == 0 && !truncated, "files": files, "truncated": truncated}, nil
}

func (f *Files) GitDiff(ctx context.Context, path string, staged bool, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 || maxBytes > gitMaxOutput {
		return "", false, protocol.E("invalid_params", "maxBytes 必须在 1 到 2MiB 之间")
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	dir, err := f.gitRepository(ctx, path)
	if err != nil {
		return "", false, gitQueryError(err)
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--submodule=short"}
	if staged {
		args = append(args, "--cached")
	}
	var out bytes.Buffer
	truncated, code, err := f.runGit(ctx, dir, maxBytes, &out, args...)
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, protocol.E("pi_error", "Git 差异查询失败")
	}
	return out.String(), truncated, nil
}
