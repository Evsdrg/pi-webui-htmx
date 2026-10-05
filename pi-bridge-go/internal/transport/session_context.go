package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
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
// 两个来源，按可信度排序：
//  1. 桥内捕获扩展记录的「实际下发给模型」的载荷（扩展改写之后）——每轮 provider
//     请求后刷新，是真正发出去的那份。export_html 拿不到它，因为 Pi 每轮结束都会
//     把 AgentState.systemPrompt 复位成基线。
//  2. export_html 快照里的基线（扩展改写之前）——仅用于尚未产生任何请求的新会话，
//     返回值标成 baseline，面板据此如实标注。
//
// 走 export_html 不是一次"查询"：Pi 的 RPC 没有暴露这两项的命令，唯一带着它们的
// 出口是 export_html。因此桥导出到自己的临时目录、读回、删掉临时文件——不让这类
// 内部产物出现在导出目录里，也就不会被 /ui/exports 下载到。
func (s *Server) sessionContext(ctx context.Context, sessionID string) (presentation.SessionContext, error) {
	worker, err := s.manager.Get(sessionID)
	if err != nil {
		return presentation.SessionContext{}, err
	}
	if captured, ok := worker.CapturedRequest(sessionID); ok {
		tools := make([]presentation.ToolInfo, 0, len(captured.Tools))
		for _, tool := range captured.Tools {
			if tool.Name == "" {
				continue
			}
			tools = append(tools, presentation.ToolInfo{
				Name:        tool.Name,
				Description: strings.TrimSpace(tool.Description),
				Parameters:  tool.Parameters,
			})
		}
		return presentation.SessionContext{SystemPrompt: captured.SystemPrompt, Tools: tools, Source: "request"}, nil
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
	value.Source = "baseline"
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

// statsMeta 汇总「这次会话是什么」的稳定事实。
// 会话文件与名称来自 Pi 的 get_state；分支来自只读的 git.status，
// 取不到就当没有——项目不在 Git 仓库里不该让整个面板失败。
func (s *Server) statsMeta(ctx context.Context, worker *run.Worker) presentation.StatsMeta {
	var meta presentation.StatsMeta
	if state, err := worker.State(ctx); err == nil {
		meta.Name = state.SessionName
		meta.File = state.SessionFile
		meta.ID = state.SessionID
		meta.Thinking = state.ThinkingLevel
		if state.Model != nil && state.Model.Provider != "" && !strings.EqualFold(state.Model.Provider, "unknown") {
			meta.Model = state.Model.Name
			if meta.Model == "" {
				meta.Model = state.Model.Provider + "/" + state.Model.ID
			}
		}
	}
	if info := worker.Info(); info.ToolPreset != "" {
		meta.Preset = info.ToolPreset
	} else {
		meta.Preset = "default"
	}
	if cwd := worker.Info().Cwd; cwd != "" {
		meta.Cwd = cwd
		if status, err := s.files.GitStatus(ctx, cwd); err == nil && status.Branch != "" {
			meta.Branch = status.Branch
		}
	}
	return meta
}
