package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"pi-bridge-go/internal/protocol"
)

// BashResult 是一条 bash 命令的终态。
type BashResult struct {
	Output         string `json:"output"`
	ExitCode       *int   `json:"exitCode"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

// Bash 执行一条 shell 命令并把输出并入会话上下文。
// requestID 用于把 bash_execution_update 事件关联回调用方。
// 注意：Pi 只在「下一次 prompt」时才把输出送进上下文，不是立即生效。
func (w *Worker) Bash(ctx context.Context, requestID, command string, excludeFromContext bool) (BashResult, error) {
	if command == "" {
		return BashResult{}, protocol.E("invalid_params", "command 不能为空")
	}
	if len(command) > 64<<10 {
		return BashResult{}, protocol.E("invalid_params", "command 过长")
	}
	fields := map[string]any{"command": command, "excludeFromContext": excludeFromContext}
	if requestID != "" {
		fields["id"] = requestID
	}
	raw, err := w.call(ctx, "bash", fields, true)
	if err != nil {
		return BashResult{}, err
	}
	var out BashResult
	if json.Unmarshal(raw, &out) != nil {
		return BashResult{}, protocol.E("pi_error", "Pi 返回的 bash 结果无效")
	}
	return out, nil
}

// AbortBash 中止正在执行的 bash 命令。
func (w *Worker) AbortBash(ctx context.Context) error {
	_, err := w.call(ctx, "abort_bash", nil, true)
	return err
}

// ReadBashOutput 读取被截断的完整 bash 输出。
// 只允许读取 Pi 自己生成的临时输出文件，且必须位于系统临时目录内，
// 避免借这个接口变成任意文件读取。
func (w *Worker) ReadBashOutput(ctx context.Context, path string, maxBytes int) (string, bool, error) {
	if path == "" {
		return "", false, protocol.E("invalid_params", "path 不能为空")
	}
	if maxBytes <= 0 || maxBytes > 8<<20 {
		return "", false, protocol.E("invalid_params", "maxBytes 必须在 1 到 8MiB 之间")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", false, protocol.E("invalid_params", "path 必须是绝对路径")
	}
	// 解析符号链接后再校验，防止链接逃逸。
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", false, protocol.E("not_found", "文件不存在")
	}
	if !isPiTempOutput(real) {
		return "", false, protocol.E("forbidden", "只允许读取 Pi 生成的 bash 输出文件")
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return "", false, protocol.E("not_found", "文件不存在或不是普通文件")
	}
	truncated := st.Size() > int64(maxBytes)
	f, err := os.Open(real)
	if err != nil {
		return "", false, protocol.E("not_found", "无法读取文件")
	}
	defer f.Close()
	if truncated {
		if _, err := f.Seek(-int64(maxBytes), 2); err != nil {
			return "", false, protocol.E("pi_error", "无法定位文件尾部")
		}
	}
	// 只分配真正会读到的字节数。以前不论文件多大都先 make(maxBytes)
	// （上限 8 MiB），读一个几 KB 的日志就白占 8 MiB——而这个接口正是
	// 「输出被截断了，把完整版读回来」，调用点在看到截断提示之后。
	want := int64(maxBytes)
	if st.Size() < want {
		want = st.Size()
	}
	buf := make([]byte, want)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", false, protocol.E("pi_error", "读取失败")
	}
	// string(buf[:n]) 是一次无法避免的拷贝：返回值是可变的 string，
	// 不能与 buf 共享底层数组（调用方会把它交给 JSON 编码器）。
	return string(buf[:n]), truncated, nil
}

// isPiTempOutput 判断路径是否为 Pi 的 bash 临时输出。
// Pi 使用形如 /tmp/pi-bash-<id>.log 的文件名。
func isPiTempOutput(real string) bool {
	dir := filepath.Dir(real)
	if dir != filepath.Clean(os.TempDir()) {
		return false
	}
	base := filepath.Base(real)
	if !strings.HasPrefix(base, "pi-bash-") {
		return false
	}
	return strings.HasSuffix(base, ".log")
}
