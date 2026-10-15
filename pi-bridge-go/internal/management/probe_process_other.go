//go:build !linux

package management

import "os/exec"

// prepareProbeProcess 在非 Linux 平台不做进程组隔离。
func prepareProbeProcess(cmd *exec.Cmd) {}

// killProbeGroup 尽力终止子进程；非 Linux 平台不做进程组回收。
func killProbeGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
