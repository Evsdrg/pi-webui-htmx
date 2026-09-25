// Package storage 保存桥自身的少量元数据。
// 只存命令回执，绝不复制会话正文。
package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/jsonl"
)

// Outcome 是一条命令的最终结果类别。
type Outcome string

const (
	OutcomeOK       Outcome = "ok"
	OutcomeError    Outcome = "error"
	OutcomeUnknown  Outcome = "unknown"
	OutcomeRejected Outcome = "rejected"
)

// Receipt 记录一条命令的回执，用于跨重启去重与对账。
type Receipt struct {
	RequestID string    `json:"requestId"`
	SessionID string    `json:"sessionId,omitempty"`
	Method    string    `json:"method"`
	Outcome   Outcome   `json:"outcome"`
	At        time.Time `json:"at"`
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

func (r *Receipts) Degraded() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.degraded
}

// currentPath 返回当前日志路径；轮转时递增序号。
func (r *Receipts) currentPath() string {
	return filepath.Join(r.dir, "receipts.jsonl")
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
		// 当前文件排在轮转文件之前，轮转文件按序号倒序。
		a, b := paths[i], paths[j]
		if filepath.Base(a) == "receipts.jsonl" {
			return true
		}
		if filepath.Base(b) == "receipts.jsonl" {
			return false
		}
		return a > b
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

// put 写入内存索引并按条数淘汰；调用方需持有锁。
func (r *Receipts) put(rec Receipt) {
	if _, exists := r.byID[rec.RequestID]; !exists {
		r.order = append(r.order, rec.RequestID)
	}
	r.byID[rec.RequestID] = rec
	for len(r.order) > r.limits.MaxEntries {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.byID, oldest)
	}
}

// Record 追加一条回执。存储未启用时安全退化为空操作。
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
		return os.ErrClosed
	}
	if r.written+int64(len(b)) > r.limits.MaxFileBytes {
		if err := r.rotateLocked(); err != nil {
			r.degraded = true
			// 轮转失败不阻塞命令，只降级。
		}
	}
	if r.file != nil {
		if _, err := r.file.Write(b); err != nil {
			r.degraded = true
		} else {
			r.written += int64(len(b))
		}
	}
	r.put(rec)
	return nil
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
