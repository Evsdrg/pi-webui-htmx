// Package terminal 管理交互式 shell 的 PTY 会话。
// 与 Pi 的 bash 工具无关：这里是给人用的终端，不是给模型的。
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"

	"pi-bridge-go/internal/childenv"
	"pi-bridge-go/internal/protocol"
)

// ErrClosed 表示终端已关闭。
var ErrClosed = errors.New("终端已关闭")

// Config 是终端管理器的限额。
type Config struct {
	MaxTerminals int
	IdleTimeout  time.Duration
	ReadBuffer   int
	MaxCols      uint16
	MaxRows      uint16
}

// Defaults 给出默认限额。
func Defaults() Config {
	return Config{MaxTerminals: 4, IdleTimeout: 10 * time.Minute, ReadBuffer: 32 << 10, MaxCols: 500, MaxRows: 200}
}

// Info 是终端的对外描述。
type Info struct {
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	PID    int    `json:"pid"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	Closed bool   `json:"closed"`
}

// Terminal 是一个 PTY 会话。
type Terminal struct {
	id   string
	cwd  string
	cmd  *exec.Cmd
	ptmx *os.File
	cols uint16
	rows uint16
	// maxCols/maxRows 与 Open 用同一组上限：Resize 不能成为绕过它的口子，
	// 否则 65535×65535 会让 PTY 按这个尺寸分配渲染缓冲（B79）。
	maxCols uint16
	maxRows uint16
	closed  atomic.Bool
	lastUse time.Time
	done    chan struct{}
	// pumpDone 在读取循环退出后关闭（PTY 里可读的数据已排空）。
	// Wait goroutine 靠它决定何时可以安全关闭 ptmx。
	pumpDone chan struct{}
	subs     map[*Subscription]struct{}
	mu       sync.Mutex
}

// pumpDrainTimeout 是进程退出后等待 pump 排空 PTY 的上限。
//
// shell 退出后 PTY 缓冲里往往还有未读输出（最后一条命令的结果），
// 立即关闭 ptmx 会打断正在进行的 Read 并把这段输出一起丢掉。
// 正常情况 pump 读到 EIO 即刻退出，等它几乎是瞬时的；
// 只有后台作业仍持有 PTY 时读不到 EIO，靠这个上限兜底。
const pumpDrainTimeout = 200 * time.Millisecond

// Subscription 是某个连接对终端输出的订阅。
type Subscription struct {
	term    *Terminal
	ch      chan []byte
	bytes   atomic.Int64
	once    sync.Once
	maxMsgs int
	maxByte int64
}

// Next 取下一段输出；订阅被关闭时返回错误。
func (s *Subscription) Next(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case b, ok := <-s.ch:
		if !ok {
			return nil, ErrClosed
		}
		s.bytes.Add(-int64(len(b)))
		return b, nil
	}
}

// Close 只解除订阅，不关闭终端。
func (s *Subscription) Close() {
	s.once.Do(func() {
		t := s.term
		t.mu.Lock()
		if _, ok := t.subs[s]; ok {
			delete(t.subs, s)
			close(s.ch)
		}
		t.mu.Unlock()
	})
}

// Manager 管理全部 PTY 会话，并负责空闲回收。
type Manager struct {
	cfg       Config
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	startMu   sync.Mutex
	terminals map[string]*Terminal
	closed    bool
}

// Limits 返回终端的实际限额，供能力发现使用。
// 以前发现端点里写死了默认值 4 与 600，而 CLI 的 --max-terminals /
// --terminal-idle 可以改掉它们，于是「报告给客户端的限额」与「真正执行的
// 限额」是两回事（B80）。
func (m *Manager) Limits() (terminals int, idleSeconds int) {
	return m.cfg.MaxTerminals, int(m.cfg.IdleTimeout / time.Second)
}

// NewManager 构造终端管理器。
func NewManager(cfg Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfg: cfg, ctx: ctx, cancel: cancel, terminals: map[string]*Terminal{}}
	go m.reap()
	return m
}

// Context 返回管理器上下文。
func (m *Manager) Context() context.Context { return m.ctx }

// Close 关闭全部终端并等待退出。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	terms := make([]*Terminal, 0, len(m.terminals))
	for _, t := range m.terminals {
		terms = append(terms, t)
	}
	m.mu.Unlock()
	m.cancel()
	// 等待启动临界区结束，避免关闭返回后仍有尚未登记的进程。
	m.startMu.Lock()
	defer m.startMu.Unlock()
	var wg sync.WaitGroup
	for _, t := range terms {
		wg.Add(1)
		go func() { defer wg.Done(); _ = t.ForceClose() }()
	}
	wg.Wait()
}

// List 返回当前终端列表。
func (m *Manager) List() []Info {
	m.mu.Lock()
	terms := make([]*Terminal, 0, len(m.terminals))
	for _, t := range m.terminals {
		terms = append(terms, t)
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(terms))
	for _, t := range terms {
		out = append(out, t.Info())
	}
	return out
}

// Open 在授权目录内启动一个交互式 shell。
// shell 来自本机配置，不接受网络请求指定可执行文件或参数。
func (m *Manager) Open(cwd, shell string, cols, rows uint16) (*Terminal, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, protocol.E("worker_exited", "桥正在关闭")
	}
	if len(m.terminals) >= m.cfg.MaxTerminals {
		m.mu.Unlock()
		return nil, protocol.E("limit_exceeded", "终端数量已达上限")
	}
	m.mu.Unlock()

	if cols == 0 || cols > m.cfg.MaxCols {
		cols = 80
	}
	if rows == 0 || rows > m.cfg.MaxRows {
		rows = 24
	}
	if shell == "" {
		shell = defaultShell()
	}
	resolved, err := resolveShell(shell)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	cmd := exec.Command(resolved)
	cmd.Dir = cwd
	cmd.Env = childenv.Filter(append(os.Environ(), "TERM=xterm-256color"))
	// 平台差异（Pdeathsig、进程组语义）收敛在 proc_lin.go / proc_oth.go。
	ptmx, err := startPTY(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, protocol.E("pi_error", "无法启动终端")
	}
	t := &Terminal{
		id:       hex.EncodeToString(id),
		cwd:      cwd,
		cmd:      cmd,
		ptmx:     ptmx,
		cols:     cols,
		rows:     rows,
		maxCols:  m.cfg.MaxCols,
		maxRows:  m.cfg.MaxRows,
		lastUse:  time.Now(),
		done:     make(chan struct{}),
		pumpDone: make(chan struct{}),
		subs:     map[*Subscription]struct{}{},
	}
	// 无论自然退出、用户关闭还是启动期间取消，都只有此处负责 Wait。
	go func() {
		_ = cmd.Wait()
		t.closed.Store(true)
		// 先给 pump 一个把 PTY 排空的窗口，再关闭读端。
		// 反过来（直接 Close）会丢掉 shell 退出前最后一段输出：
		// 实测单核下 200 次运行里有 88 次收不到 `echo` 的结果。
		select {
		case <-t.pumpDone:
		case <-time.After(pumpDrainTimeout):
		}
		_ = t.ptmx.Close()
		killGroup(cmd.Process.Pid, syscall.SIGKILL)
		t.mu.Lock()
		for s := range t.subs {
			delete(t.subs, s)
			close(s.ch)
		}
		t.mu.Unlock()
		m.mu.Lock()
		if m.terminals[t.id] == t {
			delete(m.terminals, t.id)
		}
		m.mu.Unlock()
		close(t.done)
	}()
	m.mu.Lock()
	if m.closed || t.closed.Load() {
		m.mu.Unlock()
		_ = t.ForceClose()
		return nil, protocol.E("worker_exited", "桥或终端已退出")
	}
	m.terminals[t.id] = t
	m.mu.Unlock()
	// pump 退出（defer）会关闭 pumpDone，Wait goroutine 据此决定何时关读端。
	go t.pump(m.cfg.ReadBuffer)
	return t, nil
}

// Get 按 ID 取终端。
func (m *Manager) Get(id string) (*Terminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.terminals[id]
	if !ok {
		return nil, protocol.E("not_found", "终端不存在")
	}
	return t, nil
}

// CloseTerminal 主动关闭终端。
func (m *Manager) CloseTerminal(id string) error {
	t, err := m.Get(id)
	if err != nil {
		return err
	}
	return t.ForceClose()
}

func (m *Manager) reap() {
	interval := m.cfg.IdleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
			m.mu.Lock()
			terms := make([]*Terminal, 0, len(m.terminals))
			for _, t := range m.terminals {
				terms = append(terms, t)
			}
			m.mu.Unlock()
			for _, t := range terms {
				t.mu.Lock()
				idle := time.Since(t.lastUse) >= m.cfg.IdleTimeout
				t.mu.Unlock()
				if idle {
					go func() { _ = t.ForceClose() }()
				}
			}
		}
	}
}

// shellCandidates 是本机固定受信路径表：shell 名 → 允许的实际可执行文件。
// 不用 PATH 查找，也不接受调用方给的任意绝对路径：
// 「PATH 上有一个叫 bash 的可执行文件」与「系统里的 /bin/bash」是两回事，
// 前者可以被任何能在 PATH 里落文件的人换成任意程序（B75）。
var shellCandidates = map[string][]string{
	"sh":         {"/bin/sh", "/usr/bin/sh"},
	"bash":       {"/bin/bash", "/usr/bin/bash"},
	"dash":       {"/bin/dash", "/usr/bin/dash"},
	"zsh":        {"/bin/zsh", "/usr/bin/zsh"},
	"ksh":        {"/bin/ksh", "/usr/bin/ksh"},
	"csh":        {"/bin/csh", "/usr/bin/csh"},
	"tcsh":       {"/bin/tcsh", "/usr/bin/tcsh"},
	"fish":       {"/usr/bin/fish", "/usr/local/bin/fish"},
	"nu":         {"/usr/bin/nu", "/usr/local/bin/nu"},
	"pwsh":       {"/usr/bin/pwsh", "/usr/local/bin/pwsh", "/opt/microsoft/powershell/7/pwsh"},
	"powershell": {"/usr/bin/powershell", "/usr/local/bin/powershell"},
}

// forbiddenShellChars 一旦出现即判定为试图注入参数。
const forbiddenShellChars = " \t\r\n;|&$`><(){}[]*?\\\"'"

// resolveShell 校验并解析 shell：拒绝参数与元字符，
// 且只从受信路径表里取实际可执行文件（B75）。
// 调用方给的绝对路径必须是表里规范路径的同一文件（os.SameFile，
// 因此 /bin → /usr/bin 这类 symlink 仍可用），否则拒绝。
func resolveShell(shell string) (string, error) {
	if shell == "" {
		return "", protocol.E("invalid_params", "shell 不能为空")
	}
	if strings.ContainsAny(shell, forbiddenShellChars) {
		return "", protocol.E("invalid_params", "shell 不能包含参数或 shell 元字符")
	}
	// 相对路径（含 "bin/sh"、"./sh"、"../bin/sh"）一律拒绝：
	// 它的解析结果取决于进程当前工作目录，等于让调用方间接挑二进制。
	// 只接受裸名字（查受信表）或绝对路径（必须是表内候选的同一文件）。
	if !filepath.IsAbs(shell) && strings.ContainsRune(shell, os.PathSeparator) {
		return "", protocol.E("invalid_params", "shell 必须是名字或绝对路径")
	}
	base := strings.ToLower(filepath.Base(shell))
	candidates, ok := shellCandidates[base]
	if !ok {
		return "", protocol.E("forbidden", "shell 不在允许列表内")
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			continue
		}
		if filepath.IsAbs(shell) && !sameFile(shell, candidate) {
			// 名字同名但路径不是受信文件：拒绝，不静默改用表里的路径。
			continue
		}
		return candidate, nil
	}
	return "", protocol.E("not_found", "找不到受信的 shell")
}

// sameFile 判断两个路径是否指向同一个文件（跟随 symlink）。
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// defaultShell 从受信表里选一个可用 shell，不读网络配置。
// SHELL 环境变量只作为「优先哪个名字」的提示，实际路径仍来自表。
func defaultShell() string {
	if env := os.Getenv("SHELL"); env != "" {
		if _, err := resolveShell(env); err == nil {
			return env
		}
	}
	for _, name := range []string{"bash", "sh"} {
		if _, err := resolveShell(name); err == nil {
			return name
		}
	}
	return "sh"
}

// ID 返回终端标识。
func (t *Terminal) ID() string { return t.id }

// Info 返回终端快照。
func (t *Terminal) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Info{ID: t.id, Cwd: t.cwd, PID: pidOf(t.cmd), Cols: t.cols, Rows: t.rows, Closed: t.closed.Load()}
}

func pidOf(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

// Subscribe 注册一个有界输出订阅。
func (t *Terminal) Subscribe(maxMsgs int, maxByte int64) (*Subscription, error) {
	if t.closed.Load() {
		return nil, ErrClosed
	}
	if maxMsgs <= 0 {
		maxMsgs = 64
	}
	if maxByte <= 0 {
		maxByte = 1 << 20
	}
	s := &Subscription{term: t, ch: make(chan []byte, maxMsgs), maxMsgs: maxMsgs, maxByte: maxByte}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed.Load() {
		return nil, ErrClosed
	}
	t.subs[s] = struct{}{}
	return s, nil
}

// pump 持续读取 PTY 输出并分发给订阅者。
// 必须始终读取，否则 shell 会因 PTY 缓冲区满而停止输出。
//
// 退出前不调用 Close：关闭流程等 t.done，而 t.done 由 Wait goroutine 在看到
// 本函数退出后才关闭，两边互等会拖到各自的超时才解开（实测 2 秒）。
// 这里只负责通知（pumpDone）与保证进程收尾。
func (t *Terminal) pump(readBuffer int) {
	defer close(t.pumpDone)
	if readBuffer <= 0 {
		readBuffer = 32 << 10
	}
	buf := make([]byte, readBuffer)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			t.dispatch(chunk)
		}
		if err != nil {
			// 读端失效：shell 自己退出了（EIO），或者 PTY 被关闭流程关掉。
			// 杀一场进程组是为了覆盖「读端坏了但 shell 还活着」这一支，
			// 让 cmd.Wait 尽快返回；正常退出时是空操作（ESRCH）。
			if t.cmd.Process != nil {
				killGroup(t.cmd.Process.Pid, syscall.SIGKILL)
			}
			return
		}
	}
}

// dispatch 投递一段输出；慢订阅者被摘除，绝不阻塞读取循环。
func (t *Terminal) dispatch(chunk []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastUse = time.Now()
	for s := range t.subs {
		if s.bytes.Add(int64(len(chunk))) > s.maxByte {
			s.bytes.Add(-int64(len(chunk)))
			delete(t.subs, s)
			close(s.ch)
			continue
		}
		select {
		case s.ch <- chunk:
		default:
			s.bytes.Add(-int64(len(chunk)))
			delete(t.subs, s)
			close(s.ch)
		}
	}
}

// Write 向终端写入输入。
func (t *Terminal) Write(data []byte) error {
	if t.closed.Load() {
		return ErrClosed
	}
	if len(data) == 0 || len(data) > 64<<10 {
		return protocol.E("invalid_params", "输入为空或过长")
	}
	t.mu.Lock()
	t.lastUse = time.Now()
	t.mu.Unlock()
	if _, err := t.ptmx.Write(data); err != nil {
		_ = t.Close()
		return protocol.E("worker_exited", "终端写入失败")
	}
	return nil
}

// Resize 调整终端尺寸。
func (t *Terminal) Resize(cols, rows uint16) error {
	if t.closed.Load() {
		return ErrClosed
	}
	if cols == 0 || rows == 0 {
		return protocol.E("invalid_params", "cols 与 rows 必须大于 0")
	}
	// 超限夹到上限，而不是像 Open 那样回落默认值：resize 反映的是当前窗口的
	// 真实尺寸，静默改成 80×24 会让用户看到终端突然缩小。
	if t.maxCols > 0 && cols > t.maxCols {
		cols = t.maxCols
	}
	if t.maxRows > 0 && rows > t.maxRows {
		rows = t.maxRows
	}
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.lastUse = time.Now()
	t.mu.Unlock()
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return protocol.E("pi_error", "调整终端尺寸失败")
	}
	return nil
}

// Done 在终端退出时关闭。
func (t *Terminal) Done() <-chan struct{} { return t.done }

// Close 分级关闭终端：关 PTY → SIGTERM 进程组 → SIGKILL。
// Close 优雅关闭终端：先关 pty，给进程组一个 SIGTERM 与宽限期。
//
// 以前这个方法是 Close(force bool)：调用点写 Close(true) / Close(false)
// 完全读不出意图，而两种语义差别很大——force 会 SIGKILL 整个进程组，
// 里面的用户进程没有机会收尾。现在拆成两个具名方法。
func (t *Terminal) Close() error { return t.close(false) }

// ForceClose 在宽限期之后强杀整个进程组，供「用户主动关闭」这类
// 不打算等进程自己退出的场合使用。
func (t *Terminal) ForceClose() error { return t.close(true) }

func (t *Terminal) close(force bool) error {
	if t.closed.Swap(true) {
		if force && t.cmd.Process != nil {
			killGroup(t.cmd.Process.Pid, syscall.SIGKILL)
			killSession(t.cmd.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-t.done:
			return nil
		case <-time.After(2 * time.Second):
			return protocol.E("timeout", "等待终端退出超时")
		}
	}
	_ = t.ptmx.Close()
	grace := 500 * time.Millisecond
	select {
	case <-t.done:
	case <-time.After(grace):
	}
	if t.cmd.Process != nil {
		// 两步：先按进程组（shell 自己那组），再按会话（job control
		// 把后台作业放进了各自的进程组，只杀组会留下它们）。
		killGroup(t.cmd.Process.Pid, syscall.SIGTERM)
		killSession(t.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-t.done:
	case <-time.After(grace):
	}
	if t.cmd.Process != nil && force {
		killGroup(t.cmd.Process.Pid, syscall.SIGKILL)
		killSession(t.cmd.Process.Pid, syscall.SIGKILL)
	}
	// 宽限期后即使 shell 已退出，也要回收残留的会话成员。
	if t.cmd.Process != nil && !force {
		killSession(t.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-t.done:
	case <-time.After(grace):
		return protocol.E("timeout", "终端进程未退出")
	}
	return nil
}
