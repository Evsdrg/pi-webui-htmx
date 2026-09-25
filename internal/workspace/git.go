package workspace

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"pi-bridge-go/internal/protocol"
)

// Git 限制：命令超时与输出字节上限，避免 diff 过大拖垮桥。
const (
	gitTimeout     = 10 * time.Second
	gitMaxOutput   = 2 << 20
	gitCommandName = "git"
)

// GitStatus 返回仓库概览：分支、是否干净、变更文件列表。
func (f *Files) GitStatus(ctx context.Context, path string) (map[string]any, error) {
	root, rel, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	dir := root
	if rel != "." {
		dir = root + "/" + rel
	}
	if !isGitRepo(ctx, dir) {
		return nil, protocol.E("not_found", "目标不是 Git 仓库")
	}
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	porcelain, err := gitOutput(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return nil, err
	}
	files := []map[string]string{}
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		files = append(files, map[string]string{
			"status": strings.TrimSpace(line[:2]),
			"path":   strings.TrimSpace(line[3:]),
		})
	}
	return map[string]any{
		"branch": strings.TrimSpace(branch),
		"clean":  len(files) == 0,
		"files":  files,
	}, nil
}

// GitDiff 返回工作区或暂存区的 diff，输出超限时截断并标记。
func (f *Files) GitDiff(ctx context.Context, path string, staged bool, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 || maxBytes > gitMaxOutput {
		return "", false, protocol.E("invalid_params", "maxBytes 必须在 1 到 2MiB 之间")
	}
	root, rel, err := f.resolve(path)
	if err != nil {
		return "", false, err
	}
	dir := root
	if rel != "." {
		dir = root + "/" + rel
	}
	if !isGitRepo(ctx, dir) {
		return "", false, protocol.E("not_found", "目标不是 Git 仓库")
	}
	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--cached")
	}
	out, err := gitOutputLimited(ctx, dir, maxBytes, args...)
	if err != nil {
		return "", false, err
	}
	return out.text, out.truncated, nil
}

type limitedOutput struct {
	text      string
	truncated bool
}

func isGitRepo(ctx context.Context, dir string) bool {
	out, err := gitOutput(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// gitOutput 执行 git 命令并返回文本输出。
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutputLimited(ctx, dir, gitMaxOutput, args...)
	if err != nil {
		return "", err
	}
	return out.text, nil
}

// gitOutputLimited 执行 git 命令并限制输出字节。
// 超限时直接判失败，不返回半截 diff 让前端误以为完整。
func gitOutputLimited(ctx context.Context, dir string, maxBytes int, args ...string) (limitedOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitCommandName, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, max: maxBytes}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return limitedOutput{}, protocol.E("timeout", "git 命令超时")
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return limitedOutput{}, protocol.E("pi_error", "git 命令失败："+msg)
	}
	truncated := false
	if stdout.Len() >= maxBytes {
		truncated = true
	}
	return limitedOutput{stdout.String(), truncated}, nil
}

// limitedWriter 在超过上限后丢弃后续字节，避免 diff 撑爆内存。
type limitedWriter struct {
	buf    *bytes.Buffer
	max    int
	dumped int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.dumped += len(p) - room
			return len(p), nil
		}
		w.buf.Write(p)
		return len(p), nil
	}
	w.dumped += len(p)
	return len(p), nil
}
