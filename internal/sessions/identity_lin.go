//go:build linux

package sessions

import (
	"os"
	"syscall"
)

// fileIdentity 返回文件的设备号与 inode，用于识别「同一个文件」。
// 原子替换（rename）会生成新的 inode，即使路径、大小、mtime 都相同也能被发现，
// 因此它比 (path, size, mtime) 更可靠。第二个返回值表示是否取得。
func fileIdentity(path string) (dev, ino uint64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		if _, serr := os.Stat(path); serr != nil {
			return 0, 0, false
		}
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
