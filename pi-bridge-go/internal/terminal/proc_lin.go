//go:build linux

package terminal

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
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

// killSession 向同一会话内的所有进程发信号。
//
// 为什么 killGroup 不够：pty 以 Setsid 启动，shell 是会话首进程；交互式
// shell 会开启 job control，把每个后台作业放进它自己的进程组。实测
// `sh` 里跑 `sleep 300 &`：shell 是 pgid=pid、sid=pid，而后台作业是
// pgid=自己的 pid、sid 仍等于 shell 的 pid。于是 killGroup(-shellPid)
// 只杀掉 shell 那一组，作业会被留下（B40 的终端侧缺口）。
//
// Linux 没有「按会话发信号」的接口，只能扫 /proc 找 sid 相同的进程。
// 只在关闭终端时调用（低频），不进任何热路径。
//
// 已知边界：sid 是 pid，理论上可能被复用。这里与 killGroup 一样，
// 只在「刚确认过 shell 仍在运行」的关闭路径上使用，窗口很短。
func killSession(pid int, sig any) {
	s, ok := sig.(syscall.Signal)
	if !ok || pid <= 0 {
		return
	}
	// 会话内所有进程，包括 shell 自己；重复发信号是无害的。
	for _, target := range sessionMembers(pid) {
		_ = syscall.Kill(target, s)
	}
}

// sessionMembers 返回与 pid 属于同一会话的进程号。
func sessionMembers(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]int, 0, 8)
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil || n <= 0 {
			continue
		}
		if sessionOf(n) == pid {
			out = append(out, n)
		}
	}
	return out
}

// sessionOf 返回进程的会话 ID（/proc/<pid>/stat 的第 6 个字段）。
//
// 解析要点：comm 字段被括号包住且**可能包含空格与右括号**（例如
// `/bin/sh -c echo )`），所以不能按空格切分整行——必须从最后一个 ')'
// 之后开始切。这是 proc(5) 明确提醒过的经典陷阱。
func sessionOf(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return -1
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return -1
	}
	fields := bytes.Fields(b[i+2:])
	// 从 ') ' 之后起，字段依次是 state(0)、ppid(1)、pgrp(2)、session(3)。
	if len(fields) < 4 {
		return -1
	}
	n, err := strconv.Atoi(string(fields[3]))
	if err != nil {
		return -1
	}
	return n
}
