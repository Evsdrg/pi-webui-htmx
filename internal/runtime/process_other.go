//go:build !linux

package runtime

import (
	"errors"
	"os/exec"
)

// prepareProcess 在 A 阶段只支持 Linux 进程监督，其他平台显式报错，
// 不提供降级的半套实现。
func prepareProcess(cmd *exec.Cmd) error {
	return errors.New("A 阶段的进程监督仅支持 Linux")
}

// signalGroup 尽力终止子进程；非 Linux 平台不做进程组回收。
func signalGroup(cmd *exec.Cmd, force bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
