package transport

import (
	"testing"
	"time"

	"pi-bridge-go/internal/presentation"
)

// 缓存必须按 epoch 失效：fork/clone 之后系统提示词与工具集会变，
// 拿旧 epoch 的快照会让面板显示上一个身份的提示词。
func Test会话元数据缓存按epoch失效(t *testing.T) {
	cache := newSessionContextCache()
	first := presentation.SessionContext{SystemPrompt: "第一代"}
	cache.put("s1", "e1", first)
	if value, ok := cache.get("s1", "e1"); !ok || value.SystemPrompt != "第一代" {
		t.Fatalf("同 epoch 应命中缓存：%+v ok=%v", value, ok)
	}
	if _, ok := cache.get("s1", "e2"); ok {
		t.Fatal("epoch 变化后不得命中旧缓存")
	}
}

// 缓存有过期时间：会话内的提示词可能随扩展重新绑定而变，
// 不能永久缓存。
func Test会话元数据缓存会过期(t *testing.T) {
	cache := newSessionContextCache()
	cache.put("s1", "e1", presentation.SessionContext{SystemPrompt: "p"})
	// 直接把时间戳往前推，避免测试里真的等待。
	cache.mu.Lock()
	entry := cache.entries["s1"]
	entry.at = time.Now().Add(-sessionContextTTL - time.Second)
	cache.entries["s1"] = entry
	cache.mu.Unlock()
	if _, ok := cache.get("s1", "e1"); ok {
		t.Fatal("超过 TTL 后不得命中缓存")
	}
}

// 缓存条数有上限：桥长期运行会见过很多会话，不淘汰就是缓慢泄漏。
func Test会话元数据缓存有上限(t *testing.T) {
	cache := newSessionContextCache()
	for i := 0; i < sessionContextCacheMax+4; i++ {
		cache.put(string(rune('a'+i)), "e1", presentation.SessionContext{SystemPrompt: "p"})
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > sessionContextCacheMax {
		t.Fatalf("缓存条数应不超过 %d，实际 %d", sessionContextCacheMax, size)
	}
}

// 取回失败的状态码要能区分「还没启动」与真正的错误：
// 前者界面要提示先启动会话，后者是故障。
func Test会话元数据错误状态码(t *testing.T) {
	if code := contextStatus(nil); code != 500 {
		t.Fatalf("未知错误应为 500，实际 %d", code)
	}
}
