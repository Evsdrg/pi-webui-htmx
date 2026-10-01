// Package storage 保存桥自身的少量元数据。
// 只存命令回执，绝不复制会话正文。
package storage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/jsonl"
)

// Outcome 是一条命令的最终结果类别。
type Outcome string

const (
	// OutcomePending 表示命令已被接受、尚未得出结论。
	// 重启时见到它只能回答 unknown：桥无法证明命令有没有到达 Pi。
	OutcomePending Outcome = "pending"
	OutcomeOK      Outcome = "ok"
	OutcomeError   Outcome = "error"
	// OutcomeUnknown 表示结果不可判定（崩溃、进程退出、回执写入失败）。
	OutcomeUnknown Outcome = "unknown"
	// OutcomeRejected 表示命令从未送达 Pi，客户端可用同一 requestId 重试。
	OutcomeRejected Outcome = "rejected"
)

// Receipt 记录一条命令的回执，用于跨重启去重与对账。
// Fingerprint 是 method+sessionId+params 的稳定散列：同一 requestId
// 携带不同内容时必须报 conflict，而不是静默复用旧结论。
type Receipt struct {
	RequestID   string    `json:"requestId"`
	SessionID   string    `json:"sessionId,omitempty"`
	Method      string    `json:"method"`
	Outcome     Outcome   `json:"outcome"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	At          time.Time `json:"at"`
}

// Limits 约束回执存储的体积。
type Limits struct {
	MaxEntries   int   // 内存中保留的回执条数
	MaxFileBytes int64 // 单个日志文件上限
	MaxRotated   int   // 保留的轮转文件数（不含当前文件）
	LineBytes    int   // 单行上限
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: 4096, MaxFileBytes: 4 << 20, MaxRotated: 3, LineBytes: 64 << 10}
}

// Receipts 是命令回执存储。
// 采用「有界内存索引 + 追加日志 + 轮转」：
//   - 启动只加载每个文件尾部，避免重启时读入全部历史；
//   - 超过上限即轮转，旧文件按数量淘汰；
//   - 写失败不影响命令执行，只记录到诊断信息。
type Receipts struct {
	dir    string
	limits Limits

	mu       sync.RWMutex
	byID     map[string]Receipt
	order    []string // 按写入顺序，用于按条数淘汰
	file     *os.File
	written  int64
	degraded bool
}

// NewReceipts 打开（或创建）回执存储。
func NewReceipts(dir string, limits Limits) (*Receipts, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r := &Receipts{dir: dir, limits: limits, byID: map[string]Receipt{}}
	if err := r.loadTail(); err != nil {
		return nil, err
	}
	// 必须先修尾部再打开追加句柄：崩溃留下的半行若不清掉，
	// 新回执会接在坏 JSON 之后，那一行永远解析失败。
	if err := r.repairTail(); err != nil {
		return nil, err
	}
	if err := r.openCurrent(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Receipts) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// currentLogName 是当前日志文件名；轮转时它会被改名成 receipts.1.jsonl。
const currentLogName = "receipts.jsonl"

// currentPath 返回当前日志路径；轮转时递增序号。
func (r *Receipts) currentPath() string {
	return filepath.Join(r.dir, currentLogName)
}

func (r *Receipts) rotatedPath(n int) string {
	return filepath.Join(r.dir, fmt.Sprintf("receipts.%d.jsonl", n))
}

func (r *Receipts) openCurrent() error {
	f, err := os.OpenFile(r.currentPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		r.degraded = true
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		r.degraded = true
		return err
	}
	r.file = f
	r.written = st.Size()
	return nil
}

// loadTail 从现有日志尾部加载回执，避免重启时全量读入。
// 载入顺序必须是「新文件优先」：当前文件最新，轮转文件按序号升序
// （receipts.1 比 receipts.2 新）。反序会让条数预算耗尽在最旧的文件上，
// 较新的 receipts.1 反而读不进来，去重表缺少近期请求。
func (r *Receipts) loadTail() error {
	paths := []string{}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			paths = append(paths, filepath.Join(r.dir, e.Name()))
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := filepath.Base(paths[i]), filepath.Base(paths[j])
		if a == b {
			return false
		}
		if a == currentLogName {
			return true
		}
		if b == currentLogName {
			return false
		}
		return rotatedIndex(a) < rotatedIndex(b)
	})
	budget := r.limits.MaxEntries
	for _, path := range paths {
		if budget <= 0 {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		// 只读尾部，控制启动成本。
		start := int64(0)
		if info.Size() > int64(r.limits.MaxEntries)*256 {
			start = info.Size() - int64(r.limits.MaxEntries)*256
		}
		if start > 0 {
			if _, err := f.Seek(start, 0); err != nil {
				_ = f.Close()
				continue
			}
			// 丢掉可能被截断的第一行。
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 64<<10), r.limits.LineBytes)
			if scanner.Scan() {
				_ = scanner.Bytes()
			}
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64<<10), r.limits.LineBytes)
		for scanner.Scan() && budget > 0 {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var rec Receipt
			if json.Unmarshal([]byte(line), &rec) != nil || rec.RequestID == "" {
				continue
			}
			r.put(rec)
			budget--
		}
		_ = f.Close()
	}
	return nil
}

// rotatedIndex 解析 receipts.N.jsonl 的序号；无法识别时排在最后。
func rotatedIndex(name string) int {
	rest, ok := strings.CutPrefix(name, "receipts.")
	if !ok {
		return int(math.MaxInt32)
	}
	num, ok := strings.CutSuffix(rest, ".jsonl")
	if !ok {
		return int(math.MaxInt32)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return int(math.MaxInt32)
	}
	return n
}

// repairTail 把当前日志截回最后一个完整换行处。
// 崩溃可能在写入中途留下半行；直接 append 会让新回执接在坏 JSON 之后，
// 该行永远无法解析，去重记录就此丢失。
func (r *Receipts) repairTail() error {
	f, err := os.OpenFile(r.currentPath(), os.O_RDWR, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil
	}
	// 单行上限已知，只需回看这么多个字节就能定位最后一条完整记录。
	window := int64(r.limits.LineBytes)
	if window <= 0 {
		window = 64 << 10
	}
	start := int64(0)
	if info.Size() > window {
		start = info.Size() - window
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return err
	}
	idx := bytes.LastIndexByte(buf, '\n')
	if idx < 0 {
		// 没有任何完整行：整个文件都不可信。
		if info.Size() > window {
			// 超出窗口仍无换行，说明存在超长坏行；不动它，交给加载逻辑忽略。
			return nil
		}
		return f.Truncate(0)
	}
	good := start + int64(idx) + 1
	if good >= info.Size() {
		return nil
	}
	return f.Truncate(good)
}

// put 写入内存索引并按条数淘汰；调用方需持有锁。
// 同一 requestId 只保留时间更晚的一条：载入顺序是新→旧，
// 运行期 pending→终态也是后写更新，旧结论不能覆盖新结论。
func (r *Receipts) put(rec Receipt) {
	if existing, ok := r.byID[rec.RequestID]; ok {
		if !rec.At.After(existing.At) {
			return
		}
		r.byID[rec.RequestID] = rec
		return
	}
	r.byID[rec.RequestID] = rec
	r.order = append(r.order, rec.RequestID)
	for len(r.order) > r.limits.MaxEntries {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.byID, oldest)
	}
}

// ErrNotEnabled 表示回执存储没有打开（未启用或已进入关闭流程）。
//
// 它不等于「命令失败」：调用方应把它看作配置事实与降级信号，
// 而不是把命令判为出错。真正的写盘失败会直接把底层错误返回上来。
var ErrNotEnabled = errors.New("回执存储未启用")

// Record 追加一条回执。
//
// 失败语义：写失败向上返回错误（调用方据此计数或告警），但内存索引仍然更新，
// 因此同一进程内的去重与对账不受影响；存储未打开时返回 ErrNotEnabled。
func (r *Receipts) Record(rec Receipt) error {
	if r == nil || rec.RequestID == "" {
		return nil
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > r.limits.LineBytes {
		return jsonl.ErrTooLarge
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		r.degraded = true
		return ErrNotEnabled
	}
	var writeErr error
	if r.written+int64(len(b)) > r.limits.MaxFileBytes {
		if rerr := r.rotateLocked(); rerr != nil {
			r.degraded = true
			// 轮转失败不阻塞命令，但要向上反映。
			writeErr = rerr
		}
	}
	if r.file != nil {
		if _, werr := r.file.Write(b); werr != nil {
			r.degraded = true
			writeErr = werr
		} else {
			r.written += int64(len(b))
		}
	}
	r.put(rec)
	return writeErr
}

// rotateLocked 轮转当前日志；调用方需持有锁。
func (r *Receipts) rotateLocked() error {
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return err
		}
		r.file = nil
	}
	// 旧文件依次后移，超出轮转上限的直接淘汰。
	for i := r.limits.MaxRotated; i >= 1; i-- {
		src := r.rotatedPath(i)
		if i >= r.limits.MaxRotated {
			_ = os.Remove(src)
			continue
		}
		dst := r.rotatedPath(i + 1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if err := os.Rename(r.currentPath(), r.rotatedPath(1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.openCurrent()
}

// Lookup 查询回执；第二个返回值表示是否命中。
func (r *Receipts) Lookup(requestID string) (Receipt, bool) {
	if r == nil || requestID == "" {
		return Receipt{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.byID[requestID]
	return rec, ok
}

// Stats 返回存储规模，用于诊断。
func (r *Receipts) Stats() map[string]any {
	if r == nil {
		return map[string]any{"enabled": false}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return map[string]any{
		"entries":  len(r.byID),
		"max":      r.limits.MaxEntries,
		"fileByte": r.written,
		"degraded": r.degraded,
	}
}
