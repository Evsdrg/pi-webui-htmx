// 工作进程日志：把 Pi 子进程的 stderr 落进一个有界、可轮转的文件。
//
// 为什么不直接接到桥自己的 stderr
// ------------------------------
// 桥以 systemd 服务运行（Restart=on-failure），进程级输出进 journald；
// 但 Pi 会话自身的报错（供应商 4xx/5xx、模型未找到、扩展加载失败）由 Pi
// 写在 stderr 上。此前它的 stderr 被指向 /dev/null，于是这些报错不进任何
// 日志——排查「某个会话为什么失败」只能靠会话 JSONL 里的投影反推。
//
// 为什么不让它自由增长
// --------------------
// Pi 的错误可能带完整 HTTP 响应体或栈回溯。若直接追加，长期运行会把磁盘
// 吃满，而且日志本身会变成故障源。因此这里做三件事：单行截断、按体积
// 轮转、按数量淘汰。总量有硬上限，超过就丢最旧的。
//
// 降级而非报错
// ------------
// 日志是尽力而为的副作用。写不进去（磁盘满、目录被删、权限变化）只能标记
// 降级并丢弃，绝不能把错误回吐给 Pi 的 stderr 管道——那会让一个观测问题
// 变成会话故障。
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// workerLogName 是当前日志文件名；轮转时它被改名成 worker.1.log。
const workerLogName = "worker.log"

// LogLimits 约束日志体积。三项共同保证总量有界。
type LogLimits struct {
	MaxFileBytes int64 // 单个文件上限，超过即轮转
	MaxRotated   int   // 保留的轮转文件数（不含当前文件）
	MaxLineBytes int   // 单行上限，超出部分截断
}

// DefaultLogLimits 给出默认上限：单文件 2 MiB、保留 2 份，合计约 6 MiB。
// 足以容纳一次会话失败的完整上下文，又不至于在长期运行里积压。
func DefaultLogLimits() LogLimits {
	return LogLimits{MaxFileBytes: 2 << 20, MaxRotated: 2, MaxLineBytes: 8 << 10}
}

// WorkerLog 是所有工作进程共享的日志落点。
//
// 多个 worker 并发写同一个文件：每一行的拼装与写入在同一把锁内完成，
// 因此不会互相撕裂（各进程分块到达的 stderr 片段在行边界上聚合）。
// 每行带 worker 标签，用来分辨并发进程。
type WorkerLog struct {
	dir    string
	limits LogLimits

	mu       sync.Mutex
	file     *os.File
	written  int64
	degraded bool
}

// NewWorkerLog 打开（或创建）日志目录。目录建不出来也不返回错误：
// 日志不可用不应阻止桥启动，调用方通过 Degraded 观察。
func NewWorkerLog(dir string, limits LogLimits) *WorkerLog {
	l := &WorkerLog{dir: dir, limits: limits}
	if err := os.MkdirAll(dir, 0700); err != nil {
		l.degraded = true
		return l
	}
	if err := l.openCurrent(); err != nil {
		l.degraded = true
	}
	return l
}

// Degraded 表示日志曾写入失败；供诊断（可接到 /healthz 之类的观测面）。
func (l *WorkerLog) Degraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degraded
}

// currentPath 返回当前日志路径；轮转时递增序号。
func (l *WorkerLog) currentPath() string {
	return filepath.Join(l.dir, workerLogName)
}

func (l *WorkerLog) rotatedPath(n int) string {
	return filepath.Join(l.dir, fmt.Sprintf("worker.%d.log", n))
}

// openCurrent 打开当前文件并记录已有体积，使重启后轮转判断仍然正确。
func (l *WorkerLog) openCurrent() error {
	f, err := os.OpenFile(l.currentPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.file = f
	l.written = st.Size()
	return nil
}

// rotateLocked 把当前文件后移一位并淘汰最旧的；调用方需持有锁。
// 序号从大到小依次让位，避免覆盖。任一步失败都按降级处理。
func (l *WorkerLog) rotateLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	// 淘汰最旧的一份：worker.{MaxRotated}.log 及更大的序号都不该存在。
	_ = os.Remove(l.rotatedPath(l.limits.MaxRotated))
	for n := l.limits.MaxRotated - 1; n >= 1; n-- {
		// worker.{n}.log → worker.{n+1}.log；不存在则跳过。
		if err := os.Rename(l.rotatedPath(n), l.rotatedPath(n+1)); err != nil && !os.IsNotExist(err) {
			l.degraded = true
		}
	}
	if err := os.Rename(l.currentPath(), l.rotatedPath(1)); err != nil && !os.IsNotExist(err) {
		l.degraded = true
	}
	if err := l.openCurrent(); err != nil {
		l.degraded = true
		return
	}
	l.written = 0
}

// writeLineLocked 落一行（已含 worker 标签与换行）。超长截断而非丢弃：
// 报错的开头往往就说明了问题。
func (l *WorkerLog) writeLineLocked(tag, line string) {
	if l.file == nil {
		l.degraded = true
		return
	}
	body := line
	limit := l.limits.MaxLineBytes
	if limit > 0 && len(body) > limit {
		body = body[:limit] + "…（已截断）"
	}
	row := fmt.Sprintf("[%s] %s\n", tag, body)
	if l.limits.MaxFileBytes > 0 && l.written+int64(len(row)) > l.limits.MaxFileBytes {
		l.rotateLocked()
		if l.file == nil {
			return // 轮转后仍打不开：已在 rotateLocked 里标记降级。
		}
	}
	n, err := l.file.WriteString(row)
	l.written += int64(n)
	if err != nil {
		l.degraded = true
	}
}

// Writer 返回一个绑定到某个 worker 标签的 io.WriteCloser，可直接作为
// exec.Cmd 的 Stderr。它按行缓冲：stderr 是字节流，片段可能在任何位置
// 切开，这里在行边界上聚合成整行再落盘（Close 时冲刷尾串）。
func (l *WorkerLog) Writer(tag string) *workerLogWriter {
	return &workerLogWriter{log: l, tag: tag}
}

type workerLogWriter struct {
	log  *WorkerLog
	tag  string
	rest []byte
	mu   sync.Mutex
}

func (w *workerLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rest = append(w.rest, p...)
	for {
		i := indexByte(w.rest, '\n')
		if i < 0 {
			break
		}
		line := string(w.rest[:i])
		w.rest = w.rest[i+1:]
		// stderr 常带 \r\n；行尾的 \r 不属于内容。
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		w.log.mu.Lock()
		w.log.writeLineLocked(w.tag, line)
		w.log.mu.Unlock()
	}
	// 一行还没结束就先攒着；但要有上限——否则一个不带换行的巨大输出
	// （例如被打印成单行的二进制内容）会先把内存撑爆，截断就来不及了。
	// 超过上限就当作一行结束落盘，余下的从新行重新累积。
	if limit := w.log.limits.MaxLineBytes; limit > 0 && len(w.rest) > limit {
		w.log.mu.Lock()
		w.log.writeLineLocked(w.tag, string(w.rest[:limit])+"…（未换行，已截断）")
		w.log.mu.Unlock()
		w.rest = w.rest[:0]
	}
	// 即使写失败也不报错：见包注释的「降级而非报错」。
	return len(p), nil
}

// Close 冲刷没有换行结尾的尾串。这意味着「最后一行没换行」的报错不会丢。
func (w *workerLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.rest) > 0 {
		w.log.mu.Lock()
		w.log.writeLineLocked(w.tag, string(w.rest))
		w.log.mu.Unlock()
		w.rest = nil
	}
	return nil
}

// indexByte 等价于 bytes.IndexByte，独自实现以省去一次切片转换。
func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
