//go:build linux

package runtime

import (
	"os/exec"
	"syscall"
)

// prepareProcess 把 Pi 放进独立进程组，并设置父进程死亡信号，
// 这样桥异常退出时不会留下无人管理的 Pi 子进程。
func prepareProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	return nil
}

// signalGroup 向整个进程组发信号。force 为真时直接强杀。
func signalGroup(cmd *exec.Cmd, force bool) {
	if cmd.Process == nil {
		return
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, signal)
}
