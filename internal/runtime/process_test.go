package runtime

import (
	"os"
	"strconv"
	"syscall"
)

// processAlive 判断进程是否仍存在，用于验证进程真的被回收。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

var _ = os.Getpid
var _ = strconv.Itoa
