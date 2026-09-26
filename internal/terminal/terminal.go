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
	id      string
	cwd     string
	cmd     *exec.Cmd
	ptmx    *os.File
	cols    uint16
	rows    uint16
	closed  atomic.Bool
	lastUse time.Time
	done    chan struct{}
	subs    map[*Subscription]struct{}
	mu      sync.Mutex
}

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
		go func() { defer wg.Done(); _ = t.Close(true) }()
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
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Setsid + Setctty 让 pty 成为受控终端；Pdeathsig 保证桥异常退出时
	// 不会留下无人管理的 shell。三者必须一起通过 StartWithAttrs 传入，
	// 因为 StartWithSize 会把 SysProcAttr 清空。
	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGTERM}
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: cols, Rows: rows}, attrs)
	if err != nil {
		return nil, protocol.E("pi_error", "无法启动终端")
	}
	t := &Terminal{
		id:      hex.EncodeToString(id),
		cwd:     cwd,
		cmd:     cmd,
		ptmx:    ptmx,
		cols:    cols,
		rows:    rows,
		lastUse: time.Now(),
		done:    make(chan struct{}),
		subs:    map[*Subscription]struct{}{},
	}
	// 无论自然退出、用户关闭还是启动期间取消，都只有此处负责 Wait。
	go func() {
		_ = cmd.Wait()
		t.closed.Store(true)
		_ = t.ptmx.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
		_ = t.Close(true)
		return nil, protocol.E("worker_exited", "桥或终端已退出")
	}
	m.terminals[t.id] = t
	m.mu.Unlock()
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
	return t.Close(true)
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
					go func() { _ = t.Close(true) }()
				}
			}
		}
	}
}

// allowedShells 是允许启动的 shell 白名单。
// 目的是让「开终端」可控，同时不把桥变成任意命令执行入口。
var allowedShells = map[string]struct{}{
	"sh": {}, "bash": {}, "dash": {}, "zsh": {}, "fish": {},
	"ksh": {}, "csh": {}, "tcsh": {}, "nu": {}, "pwsh": {}, "powershell": {},
}

// forbiddenShellChars 一旦出现即判定为试图注入参数。
const forbiddenShellChars = " \t\r\n;|&$`><(){}[]*?\\\"'"

// resolveShell 校验并解析 shell：拒绝参数与元字符，
// 只接受白名单内的可执行文件，PATH 查找或绝对路径均可。
func resolveShell(shell string) (string, error) {
	if shell == "" {
		return "", protocol.E("invalid_params", "shell 不能为空")
	}
	if strings.ContainsAny(shell, forbiddenShellChars) {
		return "", protocol.E("invalid_params", "shell 不能包含参数或 shell 元字符")
	}
	base := filepath.Base(shell)
	if _, ok := allowedShells[strings.ToLower(base)]; !ok {
		return "", protocol.E("forbidden", "shell 不在允许列表内")
	}
	resolved := shell
	if !filepath.IsAbs(shell) {
		found, err := exec.LookPath(shell)
		if err != nil {
			return "", protocol.E("not_found", "找不到指定的 shell")
		}
		resolved = found
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", protocol.E("forbidden", "shell 不是可执行的普通文件")
	}
	return resolved, nil
}

// defaultShell 按环境选择可用 shell，不读取网络配置。
func defaultShell() string {
	for _, candidate := range []string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh"} {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
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
func (t *Terminal) pump(readBuffer int) {
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
			_ = t.Close(false)
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
		_ = t.Close(false)
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
func (t *Terminal) Close(force bool) error {
	if t.closed.Swap(true) {
		if force && t.cmd.Process != nil {
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
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
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-t.done:
	case <-time.After(grace):
	}
	if t.cmd.Process != nil && force {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-t.done:
	case <-time.After(grace):
		return protocol.E("timeout", "终端进程未退出")
	}
	return nil
}
