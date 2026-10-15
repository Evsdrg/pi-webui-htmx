//go:build linux

package management

import (
	"os/exec"
	"syscall"
)

// prepareProbeProcess 把 `pi mcp list` 放进独立进程组，并设置父进程死亡信号。
// 这一点对 MCP 探测尤其重要：该命令会为每个 stdio 服务器拉起子进程，
// 只杀 pi 本身会把那些子进程留成孤儿。
func prepareProbeProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// killProbeGroup 强杀整个进程组（含 pi 拉起的 stdio 服务器子进程）。
func killProbeGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
