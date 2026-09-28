package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
)

// sessionContextTTL 是会话元数据的缓存时长。
//
// 取回系统提示词与工具定义需要让 Pi 导出一次完整 HTML（会话越大越慢），
// 而这两个值在一次工作进程生命周期内基本不变，因此按 (sessionId, epoch)
// 缓存。epoch 变化说明身份被重绑定（fork/clone/切换），缓存随之失效。
const sessionContextTTL = 120 * time.Second

// sessionContextCacheMax 限制缓存条数：桥可能长期运行并见过很多会话，
// 不设上限就是一条缓慢的内存泄漏。超出时淘汰最旧的一条。
const sessionContextCacheMax = 8

type sessionContextEntry struct {
	epoch string
	at    time.Time
	value presentation.SessionContext
}

type sessionContextCache struct {
	mu      sync.Mutex
	entries map[string]sessionContextEntry
}

func newSessionContextCache() *sessionContextCache {
	return &sessionContextCache{entries: map[string]sessionContextEntry{}}
}

func (c *sessionContextCache) get(sessionID, epoch string) (presentation.SessionContext, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok || entry.epoch != epoch || time.Since(entry.at) > sessionContextTTL {
		return presentation.SessionContext{}, false
	}
	return entry.value, true
}

func (c *sessionContextCache) put(sessionID, epoch string, value presentation.SessionContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= sessionContextCacheMax {
		var oldestKey string
		var oldestAt time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.at.Before(oldestAt) {
				oldestKey, oldestAt = key, entry.at
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[sessionID] = sessionContextEntry{epoch: epoch, at: time.Now(), value: value}
}

// sessionContext 取回会话的系统提示词与工具定义。
//
// 这不是一次"查询"：Pi 的 RPC 没有暴露这两项的命令，唯一带着它们的出口是
// export_html（它把 AgentState 的 systemPrompt/tools 写进 HTML）。因此桥导出
// 到自己的临时目录、读回、删掉临时文件——不让这类内部产物出现在导出目录里，
// 也就不会被 /ui/exports 下载到。
func (s *Server) sessionContext(ctx context.Context, sessionID string) (presentation.SessionContext, error) {
	worker, err := s.manager.Get(sessionID)
	if err != nil {
		return presentation.SessionContext{}, err
	}
	epoch := worker.Info().Epoch
	if cached, ok := s.sessionContexts.get(sessionID, epoch); ok {
		return cached, nil
	}
	dir, err := os.MkdirTemp("", "pi-bridge-ctx-")
	if err != nil {
		return presentation.SessionContext{}, protocol.E("internal", "无法创建临时目录")
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "session.html")
	if _, err := worker.ExportHTML(ctx, target); err != nil {
		return presentation.SessionContext{}, err
	}
	html, err := os.ReadFile(target)
	if err != nil {
		return presentation.SessionContext{}, protocol.E("internal", "导出文件无法读取")
	}
	value, err := presentation.ParseSessionContext(html)
	if err != nil {
		return presentation.SessionContext{}, err
	}
	s.sessionContexts.put(sessionID, epoch, value)
	return value, nil
}

// contextStatus 把会话元数据取回失败的原因映射成 HTTP 状态码。
// worker_not_running 是「还没启动」而不是请求错误，给 409 让界面能区分；
// 超限给 413，其余按内部错误处理。
func contextStatus(err error) int {
	var protocolErr *protocol.Error
	if errors.As(err, &protocolErr) {
		switch protocolErr.Code {
		case "worker_not_running":
			return 409
		case "limit_exceeded":
			return 413
		}
	}
	return 500
}
