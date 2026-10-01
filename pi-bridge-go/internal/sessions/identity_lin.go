//go:build linux

package sessions

import "syscall"

// fileIdentity 返回文件的设备号与 inode，用于识别「同一个文件」。
// 原子替换（rename）会生成新的 inode，即使路径、大小、mtime 都相同也能被发现，
// 因此它比 (path, size, mtime) 更可靠。第二个返回值表示是否取得。
//
// 取不到时一律返回 false，不再回退到 os.Stat 二次确认：两个分支的结论完全相同
// （都只能得出「不能信任这个身份」），回退只会让失败路径多一次系统调用。
// 调用方也不需要区分「不存在」与「取不到」——两者的处理都是缓存不命中。
func fileIdentity(path string) (dev, ino uint64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
