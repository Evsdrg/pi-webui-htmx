//go:build !linux

package sessions

// fileIdentity 在非 Linux 平台上退化为「无法取得」。
// 调用方必须把这种情况当作缓存不命中：宁可多扫一次，也不能用旧索引。
func fileIdentity(string) (dev, ino uint64, ok bool) { return 0, 0, false }
