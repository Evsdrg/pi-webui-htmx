// Package runtime 监督独立的 Pi 工作进程，生命周期不依赖浏览器连接。
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"pi-bridge-go/internal/childenv"
	"pi-bridge-go/internal/events"
	"pi-bridge-go/internal/goal"
	"pi-bridge-go/internal/pi"
	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/workspace"
)

type Config struct {
	Binary                                                 string
	PrefixArgs                                             []string // 仅供运维与测试追加参数，绝不允许来自网络请求
	Env                                                    []string
	AgentDir                                               string
	Store                                                  *sessions.Store
	Policy                                                 *workspace.Policy
	Extensions                                             bool
	MaxWorkers                                             int
	Metrics                                                MetricsSink
	StartTimeout, OperationTimeout, IdleTimeout, StopGrace time.Duration
	MaxFrame                                               int
	SubscriberMessages, SubscriberBytes, EventBytes        int
	ReplayItems, ReplayBytes                               int
	MaxDialogs                                             int
	// NavigateExt 是桥内会话跳转扩展的绝对路径；为空表示未启用 session.navigate。
	// NavigateResultDir 存放各 worker 的一次性结果文件（由扩展写入）。
	NavigateExt, NavigateResultDir string
	// CaptureExt 是桥内「实际载荷捕获」扩展的绝对路径；为空表示未启用。
	// CaptureResultDir 是各 worker 按会话 id 写入的捕获结果目录（由扩展写入）。
	CaptureExt, CaptureResultDir string
}

// Defaults 给出默认限额与超时（capabilities 的 replay 声明也引用它，见 transport）。
func Defaults() Config {
	return Config{Binary: "pi", MaxWorkers: 4, StartTimeout: 20 * time.Second, OperationTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, StopGrace: time.Second, MaxFrame: 8 << 20, SubscriberMessages: 32, SubscriberBytes: 1 << 20, EventBytes: 256 << 10,
		ReplayItems: 256, ReplayBytes: 1 << 20, MaxDialogs: 16}
}

// State 是 Pi 会话状态的对外投影，不包含私有凭据。
// SteeringMode / FollowUpMode / AutoCompactionEnabled 直接来自 Pi 的
// RpcSessionState，可读回；自动重试没有对应的读回字段，只能设置。
type State struct {
	SessionID           string `json:"sessionId"`
	SessionName         string `json:"sessionName,omitempty"`
	ThinkingLevel       string `json:"thinkingLevel,omitempty"`
	IsStreaming         bool   `json:"isStreaming"`
	IsCompacting        bool   `json:"isCompacting"`
	PendingMessageCount int    `json:"pendingMessageCount"`
	MessageCount        int    `json:"messageCount"`
	// SessionFile 是 Pi 当前写入的会话文件；未落盘的新建会话为空。
	SessionFile    string `json:"sessionFile,omitempty"`
	SteeringMode   string `json:"steeringMode,omitempty"`
	FollowUpMode   string `json:"followUpMode,omitempty"`
	AutoCompaction bool   `json:"autoCompactionEnabled"`
	Model          *struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		// ContextWindow 是模型的上下文窗口；0 表示 Pi 未能解析出该模型的目录信息。
		ContextWindow int `json:"contextWindow"`
	} `json:"model"`
}

// workerStatus 是工作进程的生命周期状态。
//
// 取值域是封闭的：以前它是裸 string、字面量散在 9 处赋值点上，
// 拼错一个字母不会被编译器发现，只会在界面上显示成未知状态。
// JSON 标签保持原样，因为它是协议字段（前端按这些字符串分支）。
type workerStatus string

const (
	statusStarting     workerStatus = "starting"
	statusRunning      workerStatus = "running"
	statusIdle         workerStatus = "idle"
	statusWaitingInput workerStatus = "waiting_input"
	statusStopping     workerStatus = "stopping"
	statusStopped      workerStatus = "stopped"
	statusFailed       workerStatus = "failed"
)

// String 便于日志与错误信息使用（fmt 会调用它）。
func (s workerStatus) String() string { return string(s) }

// Info 描述受管工作进程，供列表与订阅确认返回。
type Info struct {
	SessionID string       `json:"sessionId"`
	Epoch     string       `json:"epoch"`
	PID       int          `json:"pid"`
	Cwd       string       `json:"cwd"`
	Status    workerStatus `json:"status"`
	Busy      bool         `json:"busy"`
	Seq       uint64       `json:"seq"`
	// ToolPreset 是本次启动使用的工具预设；空字符串表示 Pi 默认工具集。
	ToolPreset string `json:"toolPreset,omitempty"`
}

// Manager 维护受管工作进程表，并负责空闲回收与整体关闭。
type Manager struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	// mu 保护 workers、presets、closed 三个字段与它们的读写一致性。
	// 只做短临界区（查表、增删、读标志），绝不在持锁期间启动进程或写盘——
	// 拉起进程要几十到几百毫秒，持锁就等于挡住所有会话的查表。
	mu sync.Mutex
	// startMu 串行化「启动一个 worker」，与 mu 分开是为了让启动这种
	// 慢操作不挡住其他会话的查表。加锁顺序固定为 startMu → mu：
	// 启动路径先取 startMu，再在需要改表时取 mu；反之不成立。
	startMu sync.Mutex
	workers map[string]*Worker
	// presets 记住每个会话最近一次启动使用的工具预设。空闲回收后再次
	// 启动同一会话时沿用，避免用户的选择随进程重启 silently 丢失。
	presets map[string]string
	closed  bool
	metrics MetricsSink
}

// MetricsSink 是运行时刻度接入点；为 nil 时全部退化为空操作。
type MetricsSink interface {
	WorkerStarted()
	WorkerReaped()
	WorkerExited()
	EventPublished()
	EventDropped()
	DialogsExpired(n int)
}

// New 创建管理器并启动空闲回收协程。
func New(cfg Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfg: cfg, ctx: ctx, cancel: cancel, workers: map[string]*Worker{}, presets: map[string]string{}, metrics: cfg.Metrics}
	go m.reap()
	return m
}

// Context 是管理器级上下文；命令等待基于它，浏览器断开不会取消它。
func (m *Manager) Context() context.Context { return m.ctx }

// Timeout 返回单次命令的默认等待上限。
func (m *Manager) Timeout() time.Duration { return m.cfg.OperationTimeout }

// Get 只查询已在运行的工作进程，不会隐式启动。
func (m *Manager) Get(id string) (*Worker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[id]
	if w == nil {
		return nil, protocol.E("worker_not_running", "请先显式启动会话")
	}
	return w, nil
}

// CheckRebindTarget 在身份切换之前检查目标会话是否已被其他 worker 占用。
// 放在切换前才能避免「Pi 已切换、冲突才被发现」的半套状态（B64）。
func (m *Manager) CheckRebindTarget(w *Worker, targetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.workers[targetID]; existing != nil && existing != w {
		return protocol.E("conflict", "目标会话已有工作进程")
	}
	return nil
}

// StopSession 停止某个会话的工作进程（若在运行），返回是否真的停掉了一个。
// force 透传给 Worker.Stop：忙中的 worker 只有 force 才会被强制停止。
// 删除会话文件前必须调用：否则 Pi 仍持有写入路径，文件被删后它还会继续写。
func (m *Manager) StopSession(id string, force bool) (bool, error) {
	// 进程表的所有访问都必须在 m.mu 下：并发 Start/退出清理会改写它，
	// 这里无锁读取会触发 fatal error（concurrent map read and map write）。
	m.mu.Lock()
	w := m.workers[id]
	m.mu.Unlock()
	if w == nil {
		return false, nil
	}
	if err := w.Stop(force); err != nil {
		return true, err
	}
	return true, nil
}

// List 返回当前受管工作进程状态。
func (m *Manager) List() []Info {
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	out := []Info{}
	for _, w := range ws {
		out = append(out, w.Info())
	}
	return out
}

// Start 显式启动或恢复一个工作进程，不指定工具预设（沿用 Pi 默认）。
// 列表与历史查询不会走到这里；只有客户端明确要求启动时才会拉起 Pi。
func (m *Manager) Start(ctx context.Context, id, cwd string) (*Worker, error) {
	return m.StartWithPreset(ctx, id, cwd, "")
}

// StartWithPreset 显式启动或恢复一个工作进程，并按预设裁剪可用工具。
// preset 为空表示沿用 Pi 默认工具集（read/bash/edit/write + 扩展工具）。
// 已经在跑的 worker 直接复用：预设只在拉起进程时生效，改动预设需要先停止再启动。
func (m *Manager) StartWithPreset(ctx context.Context, id, cwd, preset string) (*Worker, error) {
	// 串行化冷启动，但不长期持有进程表锁，也不阻塞取消命令。
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, protocol.E("timeout", "启动在拉起进程前被取消")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, protocol.E("worker_exited", "桥正在关闭")
	}
	if w := m.workers[id]; id != "" && w != nil {
		m.mu.Unlock()
		return w, nil
	}
	full := len(m.workers) >= m.cfg.MaxWorkers
	m.mu.Unlock()
	if full {
		return nil, protocol.E("limit_exceeded", "活跃工作进程数量已达上限")
	}
	if !ValidToolPreset(preset) {
		return nil, protocol.E("invalid_params", "未知的工具预设")
	}
	// 调用方没带预设时沿用该会话上次的选择；新会话首次启动才是 Pi 默认。
	if preset == "" && id != "" {
		m.mu.Lock()
		preset = m.presets[id]
		m.mu.Unlock()
	}
	file := ""
	if id != "" {
		h, err := m.cfg.Store.Find(ctx, id)
		if err != nil {
			return nil, err
		}
		if cwd != "" && cwd != h.Cwd {
			return nil, protocol.E("conflict", "恢复会话时 cwd 必须与会话记录一致")
		}
		cwd = h.Cwd
		file = m.cfg.Store.Path(h)
	}
	real, err := m.cfg.Policy.Directory(cwd)
	if err != nil {
		return nil, err
	}
	w, err := launch(m.cfg, real, file, preset)
	if err != nil {
		return nil, err
	}
	startctx, cancel := context.WithTimeout(m.ctx, m.cfg.StartTimeout)
	defer cancel()
	// 握手超时沿用调用方 ctx，但工作进程寿命独立于浏览器。
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	state, err := w.State(startctx)
	if err != nil {
		_ = w.Stop(true)
		return nil, err
	}
	if !sessions.ValidID(state.SessionID) || (id != "" && state.SessionID != id) {
		_ = w.Stop(true)
		return nil, protocol.E("conflict", "Pi 返回了不同的会话身份")
	}
	w.mu.Lock()
	w.id = state.SessionID
	w.owner = m
	w.status = statusIdle
	if w.active {
		w.status = statusRunning
	}
	w.mu.Unlock()
	m.mu.Lock()
	if state.SessionID != "" {
		m.presets[state.SessionID] = preset
	}
	m.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = w.Stop(true)
		return nil, protocol.E("worker_exited", "桥正在关闭")
	}
	if m.workers[state.SessionID] != nil {
		m.mu.Unlock()
		_ = w.Stop(true)
		return nil, protocol.E("conflict", "该会话已有工作进程")
	}
	m.workers[state.SessionID] = w
	if m.metrics != nil {
		m.metrics.WorkerStarted()
	}
	m.mu.Unlock()
	// 退出清理必须按「当前映射」而不是启动时的 ID：fork/clone/switch 会改键，
	// 用捕获的旧 ID 判断会让删除永不发生，注册表里留下已停止的 worker（B10）。
	go func() {
		<-w.done
		m.mu.Lock()
		for id, cur := range m.workers {
			if cur == w {
				delete(m.workers, id)
			}
		}
		m.mu.Unlock()
	}()
	return w, nil
}

// reap 定期回收空闲工作进程，让内存随进程退出真正归还。
func (m *Manager) reap() {
	interval := min(time.Second, m.cfg.IdleTimeout/2)
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
			m.mu.Lock()
			ws := make([]*Worker, 0, len(m.workers))
			for _, w := range m.workers {
				ws = append(ws, w)
			}
			m.mu.Unlock()
			now := time.Now()
			for _, w := range ws {
				// 先清理超时对话：Pi 到期会自行解决且不通知桥，
				// 不清理的话 waitingInput 永远为真，worker 永远不会被回收（B48）。
				if n := w.ExpireDialogs(now); n > 0 && m.metrics != nil {
					m.metrics.DialogsExpired(n)
				}
				w.mu.Lock()
				idle := !w.busyLocked() && !w.closing && time.Since(w.lastActivity) >= m.cfg.IdleTimeout
				w.mu.Unlock()
				if idle {
					if m.metrics != nil {
						m.metrics.WorkerReaped()
					}
					go func() { _ = w.stopIfIdle() }()
				}
			}
		}
	}
}

// Close 关闭桥时收敛所有受管进程，并等待它们退出。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Stop(true) }()
	}
	wg.Wait()
}

// queuedEvent 是等待投递给某个订阅者的事件及其占用字节数。
type queuedEvent struct {
	message protocol.Message
	bytes   int64
}

// Subscription 是一个连接对某个工作进程的订阅。
type Subscription struct {
	worker *Worker
	ch     chan queuedEvent
	bytes  atomic.Int64
	once   sync.Once
}

// Next 取下一条事件；订阅被关闭时要求客户端重新同步，而不是静默续传。
func (s *Subscription) Next(ctx context.Context) (protocol.Message, error) {
	select {
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	case item, ok := <-s.ch:
		if !ok {
			return protocol.Message{}, errors.New("订阅已关闭，需要重新同步")
		}
		s.bytes.Add(-item.bytes)
		return item.message, nil
	}
}

// Close 只解除该连接与工作进程的订阅关系，不会中断 Pi 任务。
func (s *Subscription) Close() {
	s.once.Do(func() {
		w := s.worker
		w.mu.Lock()
		if _, ok := w.subs[s]; ok {
			delete(w.subs, s)
			close(s.ch)
		}
		w.mu.Unlock()
	})
}

// Worker 是一个受管 Pi 进程及其连接、订阅与运行状态。
type Worker struct {
	cfg                                Config
	mu                                 sync.Mutex
	id, epoch, cwd                     string
	status                             workerStatus
	preset                             string
	cmd                                *exec.Cmd
	client                             *pi.Client
	done                               chan struct{}
	active, queued, uncertain, closing bool
	waitingInput                       bool
	// stopDeadline 是第一次停止流程（三档信号）的总预算终点。
	// closing 之后再次 Stop 只等到这个时刻，不再无限期按着 B50。
	stopDeadline   time.Time
	pendingDialogs map[string]json.RawMessage
	// extStatuses 是插件状态行（setStatus）的快照。挂在 worker 上而不是
	// 传输层：状态行属于「哪个进程」，按 (sessionId, epoch) 天然隔离（B36）。
	extStatuses map[string]string
	// dialogOpened 记录每个对话的登记时间，供超时清理使用。
	dialogOpened map[string]time.Time
	pending      int
	seq          uint64
	lastActivity time.Time
	subs         map[*Subscription]struct{}
	replay       *events.Ring
	owner        *Manager
	store        *sessions.Store
	// navResultPath 是本 worker 的会话跳转结果文件（扩展写入、桥读取）。
	// 为空表示 session.navigate 未启用。
	navResultPath string
	// navMu 串行化同一 worker 的 session.navigate：结果文件按 worker 共享，
	// 忙检查又是 check-then-act，两个并发跳转会互相覆盖结果文件、串线归属。
	navMu sync.Mutex
	// captureResultDir 是本 worker 的运行时捕获结果目录（按会话 id 命名文件）。
	// 为空表示未启用捕获，/ui/system 与 /ui/tools 回退到 export_html 基线。
	captureResultDir string
}

// newReplayRing 按配置构造补发环。
func newReplayRing(cfg Config) *events.Ring {
	return events.NewRing(cfg.ReplayItems, int64(cfg.ReplayBytes))
}

// busyLocked 判断是否存在进行中或结果未定的工作；调用时必须已持有锁。
func (w *Worker) busyLocked() bool {
	return w.active || w.queued || w.uncertain || w.waitingInput || w.pending > 0
}

// Info 返回工作进程快照。
func (w *Worker) Info() Info { w.mu.Lock(); defer w.mu.Unlock(); return w.infoLocked() }
func (w *Worker) infoLocked() Info {
	return Info{w.id, w.epoch, w.cmd.Process.Pid, w.cwd, w.status, w.busyLocked(), w.seq, w.preset}
}

// Replay 返回指定 epoch 内 afterSeq 之后的事件。
// epoch 不匹配或所需序号已被淘汰时返回 ok=false，调用方必须要求重新同步。
func (w *Worker) Replay(epoch string, afterSeq uint64) ([]events.Item, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return nil, false
	}
	if epoch != w.epoch {
		return nil, false
	}
	return w.replay.Replay(afterSeq)
}

// Subscribe 先注册有界队列，再返回当前 epoch 与序号，确保不丢订阅确认之后的事件。
func (w *Worker) Subscribe() (*Subscription, Info, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return nil, Info{}, protocol.E("worker_exited", "工作进程正在关闭")
	}
	if len(w.subs) >= 8 {
		return nil, Info{}, protocol.E("limit_exceeded", "该工作进程的订阅数量已达上限")
	}
	s := &Subscription{worker: w, ch: make(chan queuedEvent, w.cfg.SubscriberMessages)}
	w.subs[s] = struct{}{}
	return s, w.infoLocked(), nil
}

// SubscribeWithReplay 在同一次持锁内完成「取补发快照 + 注册订阅」。
//
// 分成 Replay() 再 Subscribe() 两次加锁会留下窗口期：两次锁之间发布的
// 事件既不在快照里，也不会进入实时订阅，重连后静默丢失（B05）。
// 合并后要么整体成功（快照 + 已注册），要么整体失败且不留下半套状态。
//
// cursor 为空表示普通订阅，不做补发。
// 返回值依次是：订阅句柄、当前状态快照、补发事件、补发是否可用。
// 补发不可用（replayed=false）时订阅仍然成立，但调用方必须要求客户端
// 重新同步——这一条不能靠位置记住，所以结果具名。
func (w *Worker) SubscribeWithReplay(epoch string, afterSeq uint64, cursor bool) (sub *Subscription, info Info, replay []events.Item, replayed bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return nil, Info{}, nil, false, protocol.E("worker_exited", "工作进程正在关闭")
	}
	var items []events.Item
	if cursor {
		if epoch != w.epoch {
			return nil, Info{}, nil, false, nil
		}
		replayed, ok := w.replay.Replay(afterSeq)
		if !ok {
			// 补发环已淘汰所需序号：不得注册订阅后让客户端以为能续上。
			return nil, Info{}, nil, false, nil
		}
		items = replayed
	}
	if len(w.subs) >= 8 {
		return nil, Info{}, nil, false, protocol.E("limit_exceeded", "该工作进程的订阅数量已达上限")
	}
	s := &Subscription{worker: w, ch: make(chan queuedEvent, w.cfg.SubscriberMessages)}
	w.subs[s] = struct{}{}
	return s, w.infoLocked(), items, true, nil
}

// SubscriberCount 返回当前订阅者数量，供测试与诊断核对配额是否被释放。
func (w *Worker) SubscriberCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.subs)
}

// publishLocked 向所有订阅者投递事件；慢订阅者会被摘除并关闭，绝不阻塞 Pi 输出。
func (w *Worker) publishLocked(event string, data any) {
	w.seq++
	message := protocol.Message{Version: 1, Kind: "event", SessionID: w.id, StreamID: "worker", Epoch: w.epoch, Seq: w.seq, Event: event, Data: data}
	b, err := json.Marshal(message)
	if err != nil {
		// 单条事件序列化失败不应影响会话本身，记为控制事件并继续。
		w.seq--
		return
	}
	n := int64(len(b))
	if w.cfg.Metrics != nil {
		w.cfg.Metrics.EventPublished()
	}
	w.replay.Push(w.seq, b)
	for s := range w.subs {
		if s.bytes.Add(n) > int64(w.cfg.SubscriberBytes) {
			s.bytes.Add(-n)
			delete(w.subs, s)
			close(s.ch)
			if w.cfg.Metrics != nil {
				w.cfg.Metrics.EventDropped()
			}
			continue
		}
		select {
		case s.ch <- queuedEvent{message, n}:
		default:
			s.bytes.Add(-n)
			delete(w.subs, s)
			close(s.ch)
			if w.cfg.Metrics != nil {
				w.cfg.Metrics.EventDropped()
			}
		}
	}
}

// event 处理来自 Pi 的原始事件，更新运行状态并转发给订阅者。
func (w *Worker) event(raw json.RawMessage) {
	var ev struct {
		Type     string   `json:"type"`
		ID       string   `json:"id"`
		Method   string   `json:"method"`
		Steering []string `json:"steering"`
		FollowUp []string `json:"followUp"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	// 汉化 pi-goal-x 的界面文案（状态行/通知/对话框）。只改命中的文案，未命中的
	// 原样放行。放在取锁之前做，避免持锁做一次 JSON 往返；快照与增量会用同一份
	// 译文，两处显示不会不一致。
	if ev.Type == "extension_ui_request" {
		if localized, ok := goal.LocalizeUIRequest(raw); ok {
			raw = localized
		}
	}
	w.mu.Lock()
	switch ev.Type {
	case "agent_start", "compaction_start", "auto_retry_start", "summarization_retry_scheduled":
		w.active = true
		w.status = statusRunning
		w.lastActivity = time.Now()
	case "agent_settled":
		w.active = false
		w.uncertain = false
		if !w.waitingInput {
			w.status = statusIdle
		}
		w.lastActivity = time.Now()
	case "queue_update":
		w.queued = len(ev.Steering)+len(ev.FollowUp) > 0
	}
	// 扩展 UI 分两类：需要人工输入的才登记为等待中，
	// 无需回执的（setStatus/setWidget/notify/setTitle/set_editor_text）
	// 必须直接转发，否则它们会被当成待回复对话，
	// 让 worker 永久停在 waiting_input 并挡住空闲回收。
	//
	// 登记必须排在体积上限检查之前：超大的 extension_ui_request 也要先
	// 占住对话，否则 Pi 在等回执而桥根本没有记录，扩展永久挂起（B67）。
	if ev.Type == "extension_ui_request" && needsDialogResponse(ev.Method) {
		if _, tracked := w.pendingDialogs[ev.ID]; !tracked {
			if len(w.pendingDialogs) >= w.cfg.MaxDialogs {
				// 超出上限时明确取消，避免 Pi 永久挂起。
				_ = w.client.Notify(map[string]any{"type": "extension_ui_response", "id": ev.ID, "cancelled": true})
				w.publishLocked("pi.event", map[string]any{"type": ev.Type, "id": ev.ID, "method": ev.Method, "reason": "对话数量超过上限，已取消"})
				w.mu.Unlock()
				return
			}
			w.pendingDialogs[ev.ID] = raw
			w.dialogOpened[ev.ID] = time.Now()
			w.waitingInput = true
			w.status = statusWaitingInput
		}
	}
	// 扩展状态行（setStatus）由 worker 自己记一份快照：它只在页面
	// 初次加载与重订阅时用来补齐，WS 增量始终是实时路径。挂在 worker 上
	// 意味着旧进程的状态不会串到新会话，也不会因无浏览器订阅而漏记（B36）。
	if ev.Type == "extension_ui_request" && ev.Method == "setStatus" {
		var st struct {
			StatusKey  string `json:"statusKey"`
			StatusText string `json:"statusText"`
		}
		if json.Unmarshal(raw, &st) == nil {
			w.applyStatusLocked(st.StatusKey, st.StatusText)
		}
	}
	if len(raw) > w.cfg.EventBytes {
		w.publishLocked("bridge.event_omitted", map[string]any{"type": ev.Type, "reason": "事件体积超过上限", "resyncRequired": true})
		w.mu.Unlock()
		return
	}
	w.publishLocked("pi.event", raw)
	w.mu.Unlock()
}

// maxStatusKeys 是单 worker 能保存的扩展状态行数上限。
// 与传输层老实现一致：64 条足够多个插件共存，且保证有界。
const maxStatusKeys = 64

// applyStatusLocked 更新扩展状态快照；空文本表示插件清除了该行。
// 调用方必须已持有 w.mu。
func (w *Worker) applyStatusLocked(key, text string) {
	if key == "" || len(key) > 128 || len(text) > 4096 {
		return
	}
	if w.extStatuses == nil {
		w.extStatuses = map[string]string{}
	}
	if text == "" {
		delete(w.extStatuses, key)
		return
	}
	if _, exists := w.extStatuses[key]; !exists && len(w.extStatuses) >= maxStatusKeys {
		// 达到上限时淘汰 key 最小的一项，行为确定（map 遍历无序）。
		keys := make([]string, 0, len(w.extStatuses))
		for k := range w.extStatuses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		delete(w.extStatuses, keys[0])
	}
	w.extStatuses[key] = text
}

// ExtensionStatuses 返回该 worker 的扩展状态快照副本与它所属的 epoch。
// epoch 让调用方把快照与订阅确认对应起来：旧 worker 的快照不会被当成
// 当前会话的状态（B36）。
func (w *Worker) ExtensionStatuses() (string, map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]string, len(w.extStatuses))
	for k, v := range w.extStatuses {
		out[k] = v
	}
	return w.epoch, out
}

// dialogMethods 是需要客户端回复的扩展 UI 方法。
// 与 Pi RPC 模式的 createExtensionUIContext 一致：
// select/confirm/input/editor 会阻塞等结果，其余都是 fire-and-forget。
var dialogMethods = map[string]struct{}{
	"select": {}, "confirm": {}, "input": {}, "editor": {},
}

// needsDialogResponse 判断某个扩展 UI 方法是否期待回执。
func needsDialogResponse(method string) bool {
	_, ok := dialogMethods[method]
	return ok
}

// call 向 Pi 发送命令。mutation 标记有副作用的命令，结果不明时置为待确认。
func (w *Worker) call(ctx context.Context, method string, fields map[string]any, mutation bool) (json.RawMessage, error) {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return nil, protocol.E("worker_exited", "工作进程正在关闭")
	}
	w.pending++
	if mutation {
		w.lastActivity = time.Now()
	}
	w.mu.Unlock()
	defer func() { w.mu.Lock(); w.pending--; w.mu.Unlock() }()
	data, err := w.client.Call(ctx, method, fields)
	if err != nil {
		var unknown *pi.UnknownOutcome
		var re *pi.RPCError
		switch {
		case errors.As(err, &unknown):
			if mutation {
				w.mu.Lock()
				w.uncertain = true
				w.mu.Unlock()
				return nil, protocol.E("outcome_unknown", "Pi 可能已接受该命令，请勿自动重试")
			}
			return nil, protocol.E("timeout", "暂时无法获取 Pi 状态")
		case errors.As(err, &re):
			return nil, protocol.E("pi_error", re.Message)
		case errors.Is(err, pi.ErrLimit):
			return nil, protocol.E("limit_exceeded", "Pi 在途命令过多")
		default:
			return nil, protocol.E("worker_exited", "Pi RPC 不可用")
		}
	}
	return data, nil
}

// State 查询 Pi 状态，并确认会话身份没有被意外改变。
// 只有这条路径会做身份校验；身份变更类命令必须用 stateAfterRebind。
func (w *Worker) State(ctx context.Context) (State, error) {
	state, err := w.stateAfterRebind(ctx)
	if err != nil {
		return State{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.id != "" && w.id != state.SessionID {
		return State{}, protocol.E("conflict", "Pi 改变了会话身份，请停止该工作进程")
	}
	return state, nil
}

// stateAfterRebind 读取状态但不校验身份。
// fork/clone/switch/new 之后 Pi 的会话 ID 本来就会变，
// 那些命令靠它拿到新身份，再交给 Manager.Rebind 原子入表。
func (w *Worker) stateAfterRebind(ctx context.Context) (State, error) {
	w.mu.Lock()
	seq := w.seq
	w.mu.Unlock()
	raw, err := w.call(ctx, "get_state", nil, false)
	if err != nil {
		return State{}, err
	}
	var state State
	if json.Unmarshal(raw, &state) != nil || !sessions.ValidID(state.SessionID) {
		return State{}, protocol.E("pi_error", "Pi 返回的状态无效")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq == w.seq {
		w.active = state.IsStreaming || state.IsCompacting
		w.queued = state.PendingMessageCount > 0
	}
	return state, nil
}

// Prompt 发送提示词；返回只代表 Pi 已接受，不代表任务完成。
func (w *Worker) Prompt(ctx context.Context, text, behavior string, images []map[string]any) error {
	if text == "" {
		return protocol.E("invalid_params", "提示词不能为空")
	}
	if behavior != "" && behavior != "steer" && behavior != "followUp" {
		return protocol.E("invalid_params", "streamingBehavior 无效")
	}
	fields := messageFields(text, images)
	if behavior != "" {
		fields["streamingBehavior"] = behavior
	}
	_, err := w.call(ctx, "prompt", fields, true)
	return err
}

// messageFields 组装 prompt/steer/follow_up 的公共字段。
// 图片为空时不带 images 键，避免给 Pi 发空数组。
func messageFields(text string, images []map[string]any) map[string]any {
	fields := map[string]any{"message": text}
	if len(images) > 0 {
		fields["images"] = images
	}
	return fields
}

// Abort 先清空队列再中止，否则 abort 之后队列里的消息会继续执行。
func (w *Worker) Abort(ctx context.Context) (json.RawMessage, error) {
	queue, err := w.call(ctx, "clear_queue", nil, true)
	if err != nil {
		return nil, err
	}
	_, err = w.call(ctx, "abort", nil, true)
	if err == nil {
		w.mu.Lock()
		w.active = false
		w.queued = false
		w.uncertain = false
		w.status = statusIdle
		w.lastActivity = time.Now()
		w.mu.Unlock()
	}
	return queue, err
}

// Stop 主动停止工作进程；默认拒绝仍在忙的会话，force 必须显式。
// Stop 停止工作进程。force 为假时若仍在忙则拒绝并提示必须显式强停。
// force 是协议字段（session.stop 的入参），因此保留这个名字。
func (w *Worker) Stop(force bool) error { return w.stop(force, false) }

// stopIfIdle 只在空闲达到 IdleTimeout 时才停止，由回收器调用。
// 与 Stop 分开命名：以前回收器写的是 stop(false, true)，
// 两个相邻布尔读不出哪个是 force、哪个是 idleOnly。
func (w *Worker) stopIfIdle() error { return w.stop(false, true) }

// stop 执行分级停止：关闭 stdin、SIGTERM、SIGKILL，每段都有宽限期。
// 它是 Stop 与 stopIfIdle 的共同实现，请从上面两个具名入口调用。
func (w *Worker) stop(force, idleOnly bool) error {
	w.mu.Lock()
	if w.closing {
		// 已有停止流程在跑（或已跑完）。不能无条件等 done：
		// 第一次强停超时后进程可能永远不退出，那时 done 永不关闭，
		// 而调用者是回复 session.stop 的请求协程——它会连同请求号
		// 一起永久挂住（B50）。改用同一份预算作为上限。
		deadline := w.stopDeadline
		w.mu.Unlock()
		return w.awaitStopped(deadline)
	}
	if !force && w.busyLocked() {
		w.mu.Unlock()
		return protocol.E("busy", "工作进程仍在忙，必须显式使用 force")
	}
	if idleOnly && time.Since(w.lastActivity) < w.cfg.IdleTimeout {
		w.mu.Unlock()
		return nil
	}
	w.closing = true
	w.status = statusStopping
	// 先取消所有待回复对话，否则扩展会在进程退出前一直等待人工输入。
	dialogs := make([]string, 0, len(w.pendingDialogs))
	for id := range w.pendingDialogs {
		dialogs = append(dialogs, id)
	}
	w.pendingDialogs = map[string]json.RawMessage{}
	w.dialogOpened = map[string]time.Time{}
	w.waitingInput = false
	// 预算与下面的步骤数同源，不另写常量——否则改了步骤就会漂。
	steps := w.stopSteps()
	w.stopDeadline = time.Now().Add(time.Duration(len(steps)) * w.cfg.StopGrace)
	w.publishLocked("bridge.worker_state", w.infoLocked())
	w.mu.Unlock()
	for _, id := range dialogs {
		_ = w.client.Notify(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	}
	for _, step := range steps {
		step.act()
		select {
		case <-w.done:
			return nil
		case <-time.After(w.cfg.StopGrace):
		}
	}
	return protocol.E("timeout", "强杀后工作进程仍未退出")
}

// stopStep 是分级停止的一步：先发出动作，再等一个宽限期。
// 拆成数据是为了让「总预算 = 步骤数 × 宽限期」由结构保证，
// 而不是靠两处各自维护的数字对齐。
// 最后一步超时才算失败：进程在 SIGKILL 之后仍不退出，桥已经无能为力。
type stopStep struct {
	act func()
}

func (w *Worker) stopSteps() []stopStep {
	return []stopStep{
		{act: func() { w.client.CloseInput() }},
		{act: func() { signalGroup(w.cmd, false) }},
		{act: func() {
			signalGroup(w.cmd, true)
			w.client.Close()
		}},
	}
}

// awaitStopped 等进程退出，但不越过第一次停止流程的总预算。
// 预算已耗尽时不再干等：桥已经把三档信号都发过了。
func (w *Worker) awaitStopped(deadline time.Time) error {
	select {
	case <-w.done:
		return nil
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return protocol.E("timeout", "停止流程已超时，工作进程仍未退出")
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-w.done:
		return nil
	case <-timer.C:
		return protocol.E("timeout", "停止流程已超时，工作进程仍未退出")
	}
}

// launch 按运维配置启动 Pi 子进程，并接管其 stdio。
// 只接受本机配置参数，不把可执行文件路径或额外 CLI 参数暴露给网络请求。
func launch(cfg Config, cwd, file, preset string) (*Worker, error) {
	epoch := make([]byte, 16)
	if _, err := rand.Read(epoch); err != nil {
		return nil, err
	}
	args := append([]string{}, cfg.PrefixArgs...)
	args = append(args, "--mode", "rpc", "--offline", "--no-approve", "--session-dir", cfg.Store.Dir())
	if file != "" {
		args = append(args, "--session", file)
	}
	// 桥内扩展（会话内跳转）用显式 -e 下发：它不依赖 --extensions，
	// 在禁用扩展发现的实例上同样可用（-e 路径不受 --no-extensions 影响）。
	if cfg.NavigateExt != "" {
		args = append(args, "-e", cfg.NavigateExt)
	}
	// 桥内捕获扩展同样用显式 -e 下发（不受 --no-extensions 影响）。
	if cfg.CaptureExt != "" {
		args = append(args, "-e", cfg.CaptureExt)
	}
	if !cfg.Extensions {
		args = append(args, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files")
	}
	presetArgs, err := ToolPresetArgs(preset)
	if err != nil {
		return nil, err
	}
	args = append(args, presetArgs...)
	cmd := exec.Command(cfg.Binary, args...)
	cmd.Dir = cwd
	env := append(append(os.Environ(), cfg.Env...), "PI_CODING_AGENT_DIR="+cfg.AgentDir, "PI_OFFLINE=1", "PI_TELEMETRY=0")
	navResultPath := ""
	if cfg.NavigateExt != "" && cfg.NavigateResultDir != "" {
		// 每个 worker 一份结果文件：并发 worker 共用一份会互相覆盖。
		// 变量名不能用 PI_BRIDGE_ 前缀：childenv.Filter 会把它当作桥凭据剥掉，
		// 子进程就再也读不到结果路径了。
		navResultPath = filepath.Join(cfg.NavigateResultDir, hex.EncodeToString(epoch)+".json")
		env = append(env, "PI_WEBUI_NAV_RESULT="+navResultPath)
	}
	if cfg.CaptureExt != "" && cfg.CaptureResultDir != "" {
		// 按会话 id 写入结果目录，扩展与桥对同一份目录达成一致。
		env = append(env, "PI_WEBUI_CAPTURE_DIR="+cfg.CaptureResultDir)
	}
	cmd.Env = childenv.Filter(env)
	if err := prepareProcess(cmd); err != nil {
		return nil, err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = null
	if err = cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		null.Close()
		return nil, protocol.E("worker_exited", fmt.Sprintf("无法启动 Pi 可执行文件：%T", err))
	}
	inR.Close()
	outW.Close()
	null.Close()
	w := &Worker{
		cfg:            cfg,
		epoch:          hex.EncodeToString(epoch),
		cwd:            cwd,
		status:         statusStarting,
		preset:         preset,
		cmd:            cmd,
		done:           make(chan struct{}),
		lastActivity:   time.Now(),
		subs:           map[*Subscription]struct{}{},
		pendingDialogs: map[string]json.RawMessage{},
		dialogOpened:   map[string]time.Time{},
		replay:         events.NewRing(cfg.ReplayItems, int64(cfg.ReplayBytes)),
		store:          cfg.Store,
		navResultPath:  navResultPath,
	}
	if cfg.CaptureExt != "" && cfg.CaptureResultDir != "" {
		w.captureResultDir = cfg.CaptureResultDir
	}
	w.owner = nil // 由 Manager.Start 在入表前赋值
	w.client = pi.New(inW, outR, cfg.MaxFrame, w.event)
	w.client.Start()
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	go func() {
		var exitErr error
		select {
		case exitErr = <-processDone:
		case <-w.client.Done():
			signalGroup(cmd, false)
			select {
			case exitErr = <-processDone:
			case <-time.After(cfg.StopGrace):
				signalGroup(cmd, true)
				exitErr = <-processDone
			}
		}
		// 主进程退出后，子孙进程可能仍持有管道；只回收它自己的进程组。
		signalGroup(cmd, true)
		select {
		case <-w.client.Done():
		case <-time.After(cfg.StopGrace):
			w.client.Close()
		}
		w.mu.Lock()
		w.active = false
		w.queued = false
		w.uncertain = false
		w.waitingInput = false
		w.closing = true
		// 退出协程自己置 closing 时也要给一个预算：否则并发进来的 Stop 看到
		// closing=true 却拿着零值 stopDeadline，会立刻谎报 timeout——而进程
		// 其实马上就要退出（done 就在下面几行关闭）。
		if w.stopDeadline.IsZero() {
			w.stopDeadline = time.Now().Add(cfg.StopGrace)
		}
		w.status = statusStopped
		if exitErr != nil {
			w.status = statusFailed
		}
		w.publishLocked("bridge.worker_state", w.infoLocked())
		for s := range w.subs {
			delete(w.subs, s)
			close(s.ch)
		}
		w.mu.Unlock()
		if cfg.Metrics != nil {
			cfg.Metrics.WorkerExited()
		}
		if navResultPath != "" {
			// 结果文件只服务于活着的 worker，进程退出即清理。
			_ = os.Remove(navResultPath)
		}
		close(w.done)
	}()
	return w, nil
}

// nowUTC 返回当前 UTC 时间，集中一处便于测试替换。
func nowUTC() time.Time { return time.Now().UTC() }

// EventForTest 供测试直接注入一条 Pi 事件，验证扩展 UI 分类逻辑。
func (w *Worker) EventForTest(raw json.RawMessage) { w.event(raw) }
