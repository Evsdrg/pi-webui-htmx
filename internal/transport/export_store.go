package transport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 导出产物的配额。
//
// Pi 把导出的 HTML 持久写进 exportDir，每次导出新增一个文件：没有上限时，
// 反复导出（或换不同文件名）可以一直占用磁盘，而且那些文件永远不会被回收
// （B77）。上限比 TTL 更直接——它约束的是「最多占多少」，与用户是否回来
// 下载无关，也不会删掉用户正打算读的那一份。
//
// 数量上限按典型的会话 HTML 体积给足余量；字节上限用于兜住「少数几个
// 超大产物」的情形，两者都要满足。
const (
	maxExportFiles = 32
	maxExportBytes = 256 << 20
	// maxExportFileBytes 限制**单个**导出产物。写入前就按它拒绝，
	// 否则一次导出就能占满整个目录预算（B77）。
	maxExportFileBytes = 64 << 20
)

// writeFileAtomic 先写临时文件再改名：下载端点永远只会看到完整文件，
// 中途失败也不会在目录里留下半份产物（B77）。
func writeFileAtomic(target string, content string) error {
	lastDot := strings.LastIndexByte(target, '.')
	pattern := target[:lastDot] + "-*.tmp"
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(pattern))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 导出产物可能包含会话内容，权限收紧到 0600。
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// pruneExports 按修改时间从旧到新删除，直到目录同时满足数量与体积上限。
//
// 调用点在导出之前，并把 maxFiles 传成 maxExportFiles-1：先给即将写入的
// 文件腾一个空位，这样刚写出的产物不会被同一次操作删掉。
func pruneExports(dir string, maxFiles int, maxBytes int64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type candidate struct {
		name string
		size int64
		mod  time.Time
	}
	cands := make([]candidate, 0, len(entries))
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// 读不到信息就跳过：它是同目录下的其它东西，不是导出产物。
			continue
		}
		cands = append(cands, candidate{name: e.Name(), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	if len(cands) <= maxFiles && total <= maxBytes {
		return nil
	}
	// 导出产物是临时下载物，留最新的才有用。
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.Before(cands[j].mod) })
	remaining := len(cands)
	for _, c := range cands {
		if remaining <= maxFiles && total <= maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(dir, c.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		remaining--
		total -= c.size
	}
	return nil
}
