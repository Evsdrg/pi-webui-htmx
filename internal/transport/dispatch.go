// Package transport 的命令分发。
//
// 本文件按域组织 dispatchCommon 的 switch：每个 dispatch* 只认自己那一组
// 方法名，返回 (结果, 错误, 是否已处理)。未处理时上层继续尝试下一个域，
// 全部都不认才回 unsupported_method。
//
// 判据是「谁需要什么」而不是「名字前缀」：
//   - dispatchSession / dispatchRun / dispatchBash / dispatchDialogs 需要一个
//     活跃工作进程（上层已按 session.* 取好并传进来）。
//   - dispatchSessionOps（磁盘会话）、dispatchConfig、dispatchWorkspace 完全
//     不依赖 Pi 生命周期。
//   - dispatchTerminal 需要连接（回推终端输出），不需要工作进程。
//
// 事实来源提醒：方法清单有三处——SupportedMethods（能力清单）、
// protocol.specs（执行策略）与本文件的各域 switch（实际分发）。前两者由
// methods_test.go 静态核对；后者由 server_test.go 的「每个声明支持的方法
// 都不应返回 unsupported_method」逐方法实际调用兜住。新增方法时三处都要改。
package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"pi-bridge-go/internal/pi"

	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
)

// dispatchRun 处理会话的运行控制：发送消息、排队方式、思考与压缩开关、
// 重试、以及中止与停止。这些命令都作用于活跃工作进程的状态。
func (s *Server) dispatchRun(ctx context.Context, r protocol.Request, w *run.Worker) (any, error) {
	_ = ctx
	switch r.Method {
	case "session.set_queue_mode":
		var p struct {
			Kind string `json:"kind"`
			Mode string `json:"mode"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetQueueMode(ctx, p.Kind, p.Mode); err != nil {
			return nil, err
		}
		return queueModeReply{Kind: p.Kind, Mode: p.Mode}, nil

	case "session.steer":
		var p struct {
			Text   string     `json:"text"`
			Images []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.Steer(ctx, p.Text, images); err != nil {
			return nil, err
		}
		return queuedReply{Queued: true}, nil

	case "session.follow_up":
		var p struct {
			Text   string     `json:"text"`
			Images []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.FollowUp(ctx, p.Text, images); err != nil {
			return nil, err
		}
		return queuedReply{Queued: true}, nil

	case "session.compact":
		var p struct {
			Instructions string `json:"customInstructions"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Compact(ctx, p.Instructions)

	case "session.set_auto_compaction":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoCompaction(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return enabledReply{Enabled: p.Enabled}, nil

	case "session.set_auto_retry":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoRetry(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return enabledReply{Enabled: p.Enabled}, nil

	case "session.abort_retry":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		if err := w.AbortRetry(ctx); err != nil {
			return nil, err
		}
		return abortedReply{Aborted: true}, nil

	case "session.prompt":
		var p struct {
			Text     string     `json:"text"`
			Behavior string     `json:"streamingBehavior"`
			Images   []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.Prompt(ctx, p.Text, p.Behavior, images); err != nil {
			return nil, err
		}
		return acceptedReply{Accepted: true}, nil

	case "session.abort":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		q, err := w.Abort(ctx)
		return clearedQueueReply{ClearedQueue: q}, err

	case "session.stop":
		var p struct {
			Force bool `json:"force"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		err := w.Stop(p.Force)
		return stoppedReply{Stopped: err == nil}, err
	}
	return nil, errUnhandled(r.Method)
}

// dispatchSession 处理会话的状态读取与身份变更：模型、思考等级、名称、
// 分支树与从某条消息派生新会话。
func (s *Server) dispatchSession(ctx context.Context, r protocol.Request, w *run.Worker) (any, error) {
	_ = ctx
	switch r.Method {
	case "session.state":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.State(ctx)

	case "session.models":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.Models(ctx)

	case "session.set_model":
		var p struct {
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.SetModel(ctx, p.Provider, p.ModelID)

	case "session.cycle_model":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.CycleModel(ctx)

	case "session.thinking_levels":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.ThinkingLevels(ctx)

	case "session.set_thinking":
		var p struct {
			Level string `json:"level"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetThinkingLevel(ctx, p.Level); err != nil {
			return nil, err
		}
		return levelReply{Level: p.Level}, nil

	case "session.cycle_thinking":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		level, err := w.CycleThinkingLevel(ctx)
		if err != nil {
			return nil, err
		}
		return levelReply{Level: level}, nil

	case "session.stats":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.Stats(ctx)

	case "session.set_name":
		var p struct {
			Name string `json:"name"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetName(ctx, p.Name); err != nil {
			return nil, err
		}
		return renamedReply{Renamed: true}, nil

	case "session.last_assistant":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		text, err := w.LastAssistantText(ctx)
		if err != nil {
			return nil, err
		}
		return textReply{Text: text}, nil

	case "session.commands":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.Commands(ctx)

	case "session.tree":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.Tree(ctx)

	case "session.fork_messages":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return w.ForkMessages(ctx)

	case "session.entries":
		var p struct {
			Since string `json:"since"`
			Limit int    `json:"limit"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		return w.Entries(ctx, p.Since, p.Limit)

	case "session.new":
		var p struct {
			ParentSession string `json:"parentSession"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.NewSession(ctx, p.ParentSession)
		if err != nil {
			return nil, err
		}
		return sessionIDReply{SessionID: id}, nil

	case "session.switch":
		var p struct {
			SessionPath string `json:"sessionPath"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.SwitchSession(ctx, p.SessionPath)
		if err != nil {
			return nil, err
		}
		return sessionIDReply{SessionID: id}, nil

	case "session.fork":
		var p struct {
			EntryID string `json:"entryId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Fork(ctx, p.EntryID)

	case "session.clone":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		id, err := w.Clone(ctx)
		if err != nil {
			return nil, err
		}
		return sessionIDReply{SessionID: id}, nil
	}
	return nil, errUnhandled(r.Method)
}

// dispatchSessionOps 处理磁盘上的会话操作：全文搜索、删除、导出。
// 它们不需要活跃工作进程。
func (s *Server) dispatchSessionOps(ctx context.Context, r protocol.Request) (any, error) {
	_ = ctx
	switch r.Method {
	case "sessions.search":
		var p struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Limit <= 0 {
			p.Limit = 50
		}
		limits := sessions.DefaultSearchLimits()
		limits.MaxMatches = p.Limit
		return s.store.Search(ctx, p.Query, limits)

	case "sessions.delete":
		var p struct {
			SessionID string `json:"sessionId"`
			Force     bool   `json:"force"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		// 删除前先停掉该会话的工作进程：Pi 仍持有写入路径时删文件，
		// 它会在删除后继续写入，造成幽灵会话与双写（B08）。
		// force 只影响「是否强制停止忙中的 worker」，不跳过协调本身。
		stopped, err := s.manager.StopSession(p.SessionID)
		if err != nil {
			if !p.Force {
				return nil, protocol.E("busy", "该会话仍在运行且停止失败，请确认后带 force 重试")
			}
			// force 下仍需尽力再停一次，避免明知会双写还继续删。
			if _, ferr := s.manager.StopSession(p.SessionID); ferr != nil {
				return nil, ferr
			}
		}
		result, derr := s.store.Delete(ctx, p.SessionID)
		if derr != nil {
			return nil, derr
		}
		// 两条路径返回同一个类型：只多一个 stoppedWorker 标记。
		// 以前这里是另拼一个 map，字段名与 DeleteResult 重复，
		// 一旦上游增字段就会给出两种形状。
		return deleteReply{DeleteResult: result, StoppedWorker: stopped}, nil

	case "session.export_html":
		var p struct {
			FileName string `json:"fileName"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		name, err := safeExportName(p.FileName)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(s.exportDir, name)
		// 先按配额腾出空间再导出：导出产物是持久写入的，
		// 不设上限就能被反复导出一直占住磁盘（B77）。
		s.exportMu.Lock()
		pruneErr := pruneExports(s.exportDir, maxExportFiles-1, maxExportBytes)
		s.exportMu.Unlock()
		if pruneErr != nil {
			return nil, protocol.E("pi_error", "清理导出目录失败")
		}
		worker, err := s.manager.Get(r.SessionID)
		if err != nil {
			return nil, err
		}
		path, err := worker.ExportHTML(ctx, target)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(path) != filepath.Clean(target) {
			return nil, protocol.E("conflict", "导出路径与请求不一致")
		}
		return exportReply{Path: path}, nil
	}
	return nil, errUnhandled(r.Method)
}

// dispatchConfig 处理配置读写与供应商查询。不触碰 Pi 进程，
// 唯一的出站请求发生在 config.models.discover / config.catalog。
func (s *Server) dispatchConfig(ctx context.Context, r protocol.Request) (any, error) {
	_ = ctx
	switch r.Method {
	case "config.models":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return s.piConfig.Models()

	case "config.models.raw":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return s.piConfig.Raw()

	case "config.models.write":
		var p struct {
			Config map[string]any `json:"config"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := s.piConfig.WriteModels(p.Config); err != nil {
			return nil, err
		}
		return writtenReply{Written: true}, nil

	case "config.models.discover":
		var p struct {
			BaseURL string            `json:"baseUrl"`
			API     string            `json:"api"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		models, err := s.piConfig.Discover(ctx, p.BaseURL, p.API, p.APIKey, p.Headers, s.discovery)
		if err != nil {
			return nil, err
		}
		return modelsReply{Models: models}, nil

	case "config.models.test":
		var p struct {
			BaseURL string            `json:"baseUrl"`
			API     string            `json:"api"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.piConfig.TestConnection(ctx, p.BaseURL, p.API, p.APIKey, p.Headers, s.discovery)

	case "config.catalog":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		entries, err := s.piConfig.Catalog(ctx, s.discovery)
		if err != nil {
			return nil, err
		}
		return modelsReply{Models: entries}, nil

	case "config.packages":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		list, err := s.piConfig.Packages(ctx, s.discovery)
		if err != nil {
			return nil, err
		}
		return packagesReply{Packages: list}, nil

	case "config.settings":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return s.piConfig.Settings()

	case "config.trust":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return s.piConfig.Trust()
	}
	return nil, errUnhandled(r.Method)
}

// dispatchTerminal 处理终端进程：打开、输入、改尺寸、关闭、列出。
// 需要连接（终端输出与关闭通知要回到发起它的那条连接），不需要工作进程。
func (s *Server) dispatchTerminal(r protocol.Request, sink connSink) (any, error) {
	switch r.Method {
	case "terminal.open":
		var p struct {
			Cwd   string `json:"cwd"`
			Shell string `json:"shell"`
			Cols  uint16 `json:"cols"`
			Rows  uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Cwd == "" {
			return nil, protocol.E("invalid_params", "cwd 不能为空")
		}
		directory, err := s.files.Stat(p.Cwd)
		if err != nil {
			return nil, err
		}
		if !directory.IsDir {
			return nil, protocol.E("invalid_params", "终端工作路径必须是目录")
		}
		term, err := s.terminals.Open(p.Cwd, p.Shell, p.Cols, p.Rows)
		if err != nil {
			return nil, err
		}
		info := term.Info()
		sub, err := term.Subscribe(64, 1<<20)
		if err != nil {
			_ = term.ForceClose()
			return nil, protocol.E("worker_exited", "终端已关闭")
		}
		sink.trackTerminal(term.ID(), sub)
		connCtx := sink.connContext()
		go func() {
			defer sub.Close()
			for {
				chunk, err := sub.Next(connCtx)
				if err != nil {
					if connCtx.Err() == nil {
						sink.send(protocol.Message{Version: 1, Kind: "control", Event: "bridge.terminal_closed", Data: terminalClosedEvent{TerminalID: term.ID()}})
					}
					return
				}
				if !sink.send(protocol.Message{Version: 1, Kind: "event", Event: "terminal.output", Data: terminalOutputEvent{TerminalID: term.ID(), Data: string(chunk)}}) {
					return
				}
			}
		}()
		return terminalOpenedReply{TerminalID: info.ID, Cwd: info.Cwd, PID: info.PID, Cols: info.Cols, Rows: info.Rows}, nil

	case "terminal.input":
		var p struct {
			TerminalID string `json:"terminalId"`
			Data       string `json:"data"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Write([]byte(p.Data)); err != nil {
			return nil, err
		}
		return writtenReply{Written: true}, nil

	case "terminal.resize":
		var p struct {
			TerminalID string `json:"terminalId"`
			Cols       uint16 `json:"cols"`
			Rows       uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Resize(p.Cols, p.Rows); err != nil {
			return nil, err
		}
		return resizedReply{Resized: true}, nil

	case "terminal.close":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		sink.dropTerminal(p.TerminalID)
		if err := s.terminals.CloseTerminal(p.TerminalID); err != nil {
			return nil, err
		}
		return closedReply{Closed: true}, nil

	case "terminal.list":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return terminalsReply{Terminals: s.terminals.List()}, nil
	}
	return nil, errUnhandled(r.Method)
}

// dispatchWorkspace 处理受工作区沙箱约束的文件与 Git 读取。
func (s *Server) dispatchWorkspace(ctx context.Context, r protocol.Request) (any, error) {
	_ = ctx
	switch r.Method {
	case "files.list":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		entries, truncated, err := s.files.List(p.Path)
		if err != nil {
			return nil, err
		}
		return entriesReply{Entries: entries, Truncated: truncated}, nil

	case "files.stat":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.Stat(p.Path)

	case "files.read":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, size, err := s.files.Read(p.Path)
		if err != nil {
			return nil, err
		}
		// WS 单帧有上限：超预算时必须在这里截断并告知，
		// 而不是把超限帧发给连接层——那会直接断开整条连接（B07）。
		// 需要完整内容的调用方应改用 HTTP 的 /ui/file-text。
		if len(text) > wsTextBudget {
			text = text[:wsTextBudget]
			truncated = true
		}
		return fileTextReply{Text: text, Truncated: truncated, Size: size}, nil

	case "files.image":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		body, mime, err := s.files.Image(p.Path)
		if err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(body)
		// base64 会把体积放大约 4/3，而 WS 单帧只有 512 KiB（留出封套后是
		// wsTextBudget，与文本共用同一条预算）。超过时旧实现把整帧交给
		// 连接层静默丢掉：命令看起来卡住，用户只看到超时（B33）。
		// 这里明确拒绝，并指出不需要 base64 的那条通道。
		if len(encoded) > wsTextBudget {
			return nil, protocol.E("limit_exceeded", "图片过大，无法通过事件通道返回，请改用 /ui/file-image")
		}
		return fileImageReply{Mime: mime, Data: encoded, Size: len(body)}, nil

	case "files.index":
		var p struct {
			Path  string `json:"path"`
			Query string `json:"query"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		result, err := s.files.Index(ctx, p.Path, p.Query)
		if err != nil {
			return nil, err
		}
		return result, nil

	case "files.roots":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return rootsReply{Roots: s.files.Roots()}, nil

	case "git.status":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.GitStatus(ctx, p.Path)

	case "git.diff":
		var p struct {
			Path     string `json:"path"`
			Staged   bool   `json:"staged"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.MaxBytes == 0 {
			p.MaxBytes = 512 << 10
		}
		text, truncated, err := s.files.GitDiff(ctx, p.Path, p.Staged, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return diffReply{Diff: text, Truncated: truncated}, nil
	}
	return nil, errUnhandled(r.Method)
}

// dispatchBash 处理在会话上下文中执行一次性命令、中止它、取回输出。
func (s *Server) dispatchBash(ctx context.Context, r protocol.Request, w *run.Worker) (any, error) {
	switch r.Method {
	case "session.bash":
		var p struct {
			Command            string `json:"command"`
			ExcludeFromContext bool   `json:"excludeFromContext"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		result, err := w.Bash(ctx, r.RequestID, p.Command, p.ExcludeFromContext)
		if err != nil {
			return nil, err
		}
		return result, nil

	case "session.abort_bash":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		if err := w.AbortBash(ctx); err != nil {
			return nil, err
		}
		return abortedReply{Aborted: true}, nil

	case "session.bash_output":
		var p struct {
			Path     string `json:"path"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, err := w.ReadBashOutput(ctx, p.Path, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return textTruncatedReply{Text: text, Truncated: truncated}, nil
	}
	return nil, errUnhandled(r.Method)
}

// dispatchDialogs 处理扩展对话的回复与查询。
// 对话框条目由运行时保留，因此两者都需要活跃工作进程。
func (s *Server) dispatchDialogs(ctx context.Context, r protocol.Request, w *run.Worker) (any, error) {
	switch r.Method {
	case "session.ui_response":
		var p struct {
			ID        string  `json:"id"`
			Value     *string `json:"value"`
			Confirmed *bool   `json:"confirmed"`
			Cancelled bool    `json:"cancelled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.UIResponse(ctx, p.ID, p.Value, p.Confirmed, p.Cancelled); err != nil {
			return nil, err
		}
		return answeredReply{Answered: true}, nil

	case "session.pending_dialogs":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		// 返回完整载荷而不只是 ID：HTTP 端点要渲染对话框，
		// 需要 title/options/message 等字段。
		payloads := w.PendingDialogPayloads()
		items := make([]json.RawMessage, 0, len(payloads))
		items = append(items, payloads...)
		return dialogsReply{Dialogs: items, IDs: w.PendingDialogs()}, nil
	}
	return nil, errUnhandled(r.Method)
}

// errUnhandled 表示这一域没有这个方法，调用方继续尝试下一域。
//
// 用哨兵而不是第三个返回值：域函数里那几十处 return 就不必逐条加一个
// true，拆分这批改动因此是纯搬移，行为不可能被搬错。它**只在域函数末尾**
// 返回，任何业务错误都不会与它混淆（errors.Is 比对的是同一个变量）。
// 万一它泄漏到响应里也一眼能认出来——正常路径上不可能的字符串。
var errUnhandledSentinel = errors.New("transport: 本域未处理该方法")

func errUnhandled(method string) error { return fmt.Errorf("%w: %s", errUnhandledSentinel, method) }

// isUnhandled 判断错误是否来自「没有哪个域认领这个方法」。
func isUnhandled(err error) bool { return errors.Is(err, errUnhandledSentinel) }

// decodeEmpty 用于不接受任何参数的命令：要求 params 是空对象或缺失。
//
// 它以前是 dispatchCommon 里的闭包 empty，每个 case 各调一次；
// 拆分之后各域都要用，因此提成包级函数。
func decodeEmpty(params json.RawMessage) error { return protocol.Decode(params, &struct{}{}) }
