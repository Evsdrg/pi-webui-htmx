//go:build linux

package terminal

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// procAttrs 返回 Linux 上的进程属性。
// Setsid + Setctty 让 pty 成为受控终端；Pdeathsig 保证桥异常退出时
// 不会留下无人管理的 shell。三者必须一起经 StartWithAttrs 传入，
// 因为 StartWithSize 会把 SysProcAttr 清空。
func procAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGTERM}
}

// startPTY 以受控终端属性启动 shell。
func startPTY(cmd *exec.Cmd, size *pty.Winsize) (*os.File, error) {
	return pty.StartWithAttrs(cmd, size, procAttrs())
}

// killGroup 向整个进程组发信号。
// 负 pid 表示进程组，这样才能连 shell 派生的子进程一起回收。
func killGroup(pid int, sig any) {
	s, _ := sig.(syscall.Signal)
	_ = syscall.Kill(-pid, s)
}
