// Package observe 提供固定基数的运行指标与诊断信息。
// 约束：标签只能来自受控集合（方法名、错误码），
// 绝不由外部输入派生，否则高基数标签会造成内存无界增长。
package observe

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Methods 是允许出现指标标签的方法集合，由调用方注册。
type Methods struct {
	mu    sync.RWMutex
	names map[string]struct{}
}

// NewMethods 构造受控方法集合。
func NewMethods(names ...string) *Methods {
	m := &Methods{names: map[string]struct{}{}}
	for _, n := range names {
		m.names[n] = struct{}{}
	}
	return m
}

// Register 登记一个方法名；已满或已存在时返回 false。
func (m *Methods) Register(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.names[name]; ok {
		return true
	}
	if len(m.names) >= 64 {
		return false
	}
	m.names[name] = struct{}{}
	return true
}

func (m *Methods) known(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.names[name]
	return ok
}

// Metrics 是进程级计数器集合。
type Metrics struct {
	methods *Methods

	commandsTotal   atomic.Uint64
	commandsFailed  atomic.Uint64
	eventsTotal     atomic.Uint64
	eventsDropped   atomic.Uint64
	replayTotal     atomic.Uint64
	replayMisses    atomic.Uint64
	workersStarted  atomic.Uint64
	workersReaped   atomic.Uint64
	workersExited   atomic.Uint64
	dialogsExpired  atomic.Uint64
	sessionsIndexed atomic.Uint64
	historyRequests atomic.Uint64
	authFailures    atomic.Uint64
	receiptFailures atomic.Uint64
	startedAt       time.Time

	byMethod sync.Map // method -> *atomic.Uint64
	byError  sync.Map // code -> *atomic.Uint64
}

// NewMetrics 构造指标集合。
func NewMetrics(methods *Methods) *Metrics {
	if methods == nil {
		methods = NewMethods()
	}
	return &Metrics{methods: methods, startedAt: time.Now()}
}

func counter(m *sync.Map, key string) *atomic.Uint64 {
	if v, ok := m.Load(key); ok {
		return v.(*atomic.Uint64)
	}
	c := &atomic.Uint64{}
	actual, _ := m.LoadOrStore(key, c)
	return actual.(*atomic.Uint64)
}

// CommandStarted 记录一次命令开始。
func (x *Metrics) CommandStarted(method string) {
	x.commandsTotal.Add(1)
	if x.methods.known(method) {
		counter(&x.byMethod, method).Add(1)
	} else {
		counter(&x.byMethod, "other").Add(1)
	}
}

// CommandFailed 记录一次命令失败。
func (x *Metrics) CommandFailed(method, code string) {
	x.commandsFailed.Add(1)
	if code == "" {
		code = "unspecified"
	}
	if len(code) > 32 {
		code = code[:32]
	}
	counter(&x.byError, code).Add(1)
}

// EventPublished 记录一次事件投递。
func (x *Metrics) EventPublished() { x.eventsTotal.Add(1) }

// EventDropped 记录一次因订阅者过慢而被丢弃的事件。
// 同时实现 runtime.MetricsSink，避免 runtime 反向依赖本包。
func (x *Metrics) EventDropped() { x.eventsDropped.Add(1) }

// ReplayHit 记录一次补发。
func (x *Metrics) ReplayHit() { x.replayTotal.Add(1) }

// ReplayMiss 记录一次补发失败。
func (x *Metrics) ReplayMiss() { x.replayMisses.Add(1) }

// WorkerStarted 记录工作进程启动。
func (x *Metrics) WorkerStarted() { x.workersStarted.Add(1) }

// WorkerReaped 记录空闲回收。
func (x *Metrics) WorkerReaped() { x.workersReaped.Add(1) }

// WorkerExited 记录工作进程退出。
func (x *Metrics) WorkerExited() { x.workersExited.Add(1) }

// DialogsExpired 记录因超过自身 timeout 而被清理的扩展对话数量。
func (x *Metrics) DialogsExpired(n int) { x.dialogsExpired.Add(uint64(n)) }

// HistoryRequest 记录一次历史读取。
func (x *Metrics) HistoryRequest() { x.historyRequests.Add(1) }

// AuthFailure 记录一次鉴权失败。
func (x *Metrics) AuthFailure() { x.authFailures.Add(1) }

// ReceiptFailed 记录一次回执落盘失败。
//
// 为什么单独计数：回执是「命令是否执行过」的唯一依据，写不进去意味着
// 重启后的对账失去基础。它不影响当次命令结果，所以不能在调用点变成错误，
// 但必须能被看见——否则降级会一直存在而无人知道。
func (x *Metrics) ReceiptFailed() { x.receiptFailures.Add(1) }

// SetSessionsIndexed 设置索引中的会话数（瞬时值）。
func (x *Metrics) SetSessionsIndexed(n int) { x.sessionsIndexed.Store(uint64(n)) }

// Snapshot 输出当前指标。
func (x *Metrics) Snapshot() map[string]any {
	out := map[string]any{
		"uptimeSeconds":   int64(time.Since(x.startedAt).Seconds()),
		"commandsTotal":   x.commandsTotal.Load(),
		"commandsFailed":  x.commandsFailed.Load(),
		"eventsTotal":     x.eventsTotal.Load(),
		"eventsDropped":   x.eventsDropped.Load(),
		"replayTotal":     x.replayTotal.Load(),
		"replayMisses":    x.replayMisses.Load(),
		"workersStarted":  x.workersStarted.Load(),
		"workersReaped":   x.workersReaped.Load(),
		"workersExited":   x.workersExited.Load(),
		"dialogsExpired":  x.dialogsExpired.Load(),
		"sessionsIndexed": x.sessionsIndexed.Load(),
		"historyRequests": x.historyRequests.Load(),
		"authFailures":    x.authFailures.Load(),
		"receiptFailures": x.receiptFailures.Load(),
		"byMethod":        dumpMap(&x.byMethod),
		"byErrorCode":     dumpMap(&x.byError),
	}
	return out
}

func dumpMap(m *sync.Map) map[string]uint64 {
	out := map[string]uint64{}
	m.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Uint64).Load()
		return true
	})
	return out
}

// SortedKeys 返回排序后的键，便于稳定输出。
func SortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
