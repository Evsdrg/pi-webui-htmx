package transport

import (
	"sort"
	"sync"

	"pi-bridge-go/internal/protocol"
)

// extensionStatus 是一条扩展状态。
type extensionStatus struct {
	key  string
	text string
}

// extensionState 是扩展状态的有界快照表。
//
// 为什么需要它：setStatus 是 fire-and-forget 的 WS 推送，
// 页面刷新后就丢了。若不做快照，用户刷新页面会看到状态栏空一拍，
// 直到插件再次 setStatus。
//
// 边界：这是**传输层的展示缓存**，不是记忆。按 key 去重、有上限、
// 不清空会话数据。桥重启后从空开始，不假装持久。
type extensionState struct {
	mu      sync.Mutex
	byKey   map[string]string
	maxKeys int
}

func newExtensionState(maxKeys int) *extensionState {
	if maxKeys <= 0 {
		maxKeys = 64
	}
	return &extensionState{byKey: map[string]string{}, maxKeys: maxKeys}
}

// update 记录某个 key 的最新文本。text 为空表示该插件清除了状态。
func (e *extensionState) update(key, text string) {
	if key == "" || len(key) > 128 || len(text) > 4096 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if text == "" {
		delete(e.byKey, key)
		return
	}
	if _, exists := e.byKey[key]; !exists && len(e.byKey) >= e.maxKeys {
		// 达到上限时淘汰最早插入的一项，保证有界。
		e.evictOldestLocked()
	}
	e.byKey[key] = text
}

// snapshot 返回全部状态，按 key 排序保证渲染稳定。
func (e *extensionState) snapshot() []extensionStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	keys := make([]string, 0, len(e.byKey))
	for k := range e.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]extensionStatus, 0, len(keys))
	for _, k := range keys {
		out = append(out, extensionStatus{key: k, text: e.byKey[k]})
	}
	return out
}

// forget 清空某个会话相关的前缀状态。会话停止时调用。
func (e *extensionState) forget(key string) { e.update(key, "") }

// evictOldestLocked 淘汰最早插入的一项。map 无序，用 key 排序取最小的，
// 行为确定即可，不需要严格 FIFI。
func (e *extensionState) evictOldestLocked() {
	if len(e.byKey) == 0 {
		return
	}
	keys := make([]string, 0, len(e.byKey))
	for k := range e.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	delete(e.byKey, keys[0])
}

// parseSetStatus 从 Pi 事件里取 setStatus 的 key 与 text。
// 只处理 method == "setStatus"，其余扩展方法不进入状态表。
func parseSetStatus(raw []byte) (key, text string, ok bool) {
	var v struct {
		Type       string `json:"type"`
		Method     string `json:"method"`
		StatusKey  string `json:"statusKey"`
		StatusText string `json:"statusText"`
	}
	if err := protocol.Decode(raw, &v); err != nil {
		return "", "", false
	}
	if v.Type != "extension_ui_request" || v.Method != "setStatus" {
		return "", "", false
	}
	return v.StatusKey, v.StatusText, true
}
