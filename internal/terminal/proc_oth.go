//go:build !linux

package terminal

import (
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// errUnsupportedPlatform 让非 Linux 平台在运行期明确失败。
// Pdeathsig 是 Linux 专有能力；与 runtime/process_oth.go 一致，
// 宁可显式报错，也不假装行为等价。
var errUnsupportedPlatform = errors.New("受控终端的进程监督仅支持 Linux")

func startPTY(cmd *exec.Cmd, size *pty.Winsize) (*os.File, error) {
	return nil, errUnsupportedPlatform
}

// killGroup 在非 Linux 上无进程组语义可用。
func killGroup(pid int, sig any) {}

// killSession 在非 Linux 上无 /proc 可查，只能退化为进程组语义
// （见 killGroup）。平台差异收敛在这里，调用方不必分支。
func killSession(pid int, sig any) {}
