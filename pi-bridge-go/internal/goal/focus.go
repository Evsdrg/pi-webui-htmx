package goal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// FocusEntry 是会话 JSONL 里 pi-goal-focus 自定义条目的载荷。
// 插件用 pi.appendEntry(FOCUS_ENTRY, …) 写入，形状见 session-manager.js 的
// appendCustomEntry：{type:"custom", customType:"pi-goal-focus", data:{…}}。
type FocusEntry struct {
	Version       int    `json:"version"`
	FocusedGoalID string `json:"focusedGoalId"`
	Reason        string `json:"reason"`
	StorageRoot   string `json:"storageRoot,omitempty"`
}

// focusScanMaxBytes 限制单次会话文件扫描的规模：聚焦条目在一次会话里产生得很少，
// 但为了不因一份巨大的 JSONL 把内存/耗时拖垮，超过上限就停止扫描并返回“未知”。
const focusScanMaxBytes = 256 << 20

// focusMaxEntries 是建立「父链」索引的条数上限。聚焦目标是会话级状态，插件按
// getBranch()（当前分支）取；桥要一致就必须沿父链回溯到叶子，需要每条记录的
// id/父。这里给内存设一个上限：超过后退回「按文件顺序取最后一条聚焦」，与线性
// 会话（绝大多数）一致，只是对超大且分支过的会话不再精确。
const focusMaxEntries = 200_000

// ReadFocus 从会话文件里取出**当前分支（叶子所在父链）上**最近一次聚焦决定。
//
// 返回 (focusedGoalId, storageRoot, found)。focusedGoalId 为空串表示“取消聚焦”
// 或没有记录；found 为 false 表示文件不可读或没有聚焦条目。
//
// 必须沿父链取，不能取「文件里最后一条聚焦」：fork/分支之后，被放弃分支上的
// 聚焦会排在当前分支之后，按文件顺序会读到错的目标（插件按 getBranch 读）。
func ReadFocus(sessionFile string) (goalID, storageRoot string, found bool) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	if fi, err := file.Stat(); err != nil || fi.Size() > focusScanMaxBytes {
		return "", "", false
	}

	type node struct {
		parent string
		focus  *FocusEntry
	}
	nodes := make(map[string]node, 256)
	leaf := ""
	// 线性退化：溢出时用文件顺序的最后一条聚焦。
	lastID, lastRoot, lastFound := "", "", false
	overflow := false

	reader := bufio.NewReaderSize(file, 1<<20)
	var lineCount int
	for lineCount < 2_000_000 {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineCount++
			var entry struct {
				Type       string     `json:"type"`
				CustomType string     `json:"customType"`
				ID         string     `json:"id"`
				ParentID   string     `json:"parentId"`
				Data       FocusEntry `json:"data"`
			}
			if json.Unmarshal(bytes.TrimSpace(line), &entry) == nil && entry.ID != "" {
				n := node{parent: entry.ParentID}
				if entry.CustomType == "pi-goal-focus" {
					f := entry.Data
					n.focus = &f
					lastID, lastRoot, lastFound = f.FocusedGoalID, f.StorageRoot, true
				}
				if !overflow {
					if len(nodes) >= focusMaxEntries {
						overflow = true
					} else {
						nodes[entry.ID] = n
						leaf = entry.ID
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	if overflow {
		return lastID, lastRoot, lastFound
	}
	for id := leaf; id != ""; {
		n, ok := nodes[id]
		if !ok {
			break
		}
		if n.focus != nil {
			return n.focus.FocusedGoalID, n.focus.StorageRoot, true
		}
		id = n.parent
	}
	return "", "", false
}
