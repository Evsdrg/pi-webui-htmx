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

// ReadFocus 从会话文件里取出最近一次聚焦决定。
//
// 返回 (focusedGoalId, storageRoot, found)。focusedGoalId 为空串表示“取消聚焦”
// 或没有记录；found 为 false 表示文件不可读或没有聚焦条目。
// 逐行解析：聚焦条目是很少见的行，先用子串快速过滤，再对命中的行做 JSON 解码。
func ReadFocus(sessionFile string) (goalID, storageRoot string, found bool) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return "", "", false
	}
	defer file.Close()
	if fi, err := file.Stat(); err != nil || fi.Size() > focusScanMaxBytes {
		return "", "", false
	}

	marker := []byte("pi-goal-focus")
	reader := bufio.NewReaderSize(file, 1<<20)
	var lineCount int
	for lineCount < 2_000_000 {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineCount++
			if bytes.Contains(line, marker) {
				var entry struct {
					Type       string     `json:"type"`
					CustomType string     `json:"customType"`
					Data       FocusEntry `json:"data"`
				}
				if json.Unmarshal(bytes.TrimSpace(line), &entry) == nil && entry.CustomType == "pi-goal-focus" {
					goalID = entry.Data.FocusedGoalID
					storageRoot = entry.Data.StorageRoot
					found = true
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
	return goalID, storageRoot, found
}
