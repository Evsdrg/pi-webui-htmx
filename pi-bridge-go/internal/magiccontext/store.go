// Package magiccontext 只读访问 magic-context 的本地存储。
//
// magic-context 官方有一个「面板」——packages/dashboard，一个独立的 Tauri
// 桌面应用（SolidJS + Rust），直接开 SQLite 读 context.db。它 6 个导航项
// （Projects / Workspaces / Cache / User Directives / Config / Logs）全部
// 来自同一个库，不走 Pi 的 UI 通道。
//
// 本包按同一条路径实现：只读、不写、不注入我们自己的 Pi 扩展。刻意不做成
// Pi 的插件界面，原因是 Pi 0.85.1 的 RPC 模式会把插件注册的
// setWidget(key, factory) 工厂函数直接忽略（rpc-mode.js 里只接受字符串
// 数组），所以 magic-context 那个 todo 面板在 RPC 客户端上根本到不了。
//
// 这个库是用户的跨会话记忆，敏感度高于单个会话：本包所有查询都是写死的
// 语句，参数只作为 sqlite3 的位置参数传入，绝不拼接 SQL。
package magiccontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 库文件名与目录名。magic-context 的插件在所有平台上都用 XDG_DATA_HOME
// 或 ~/.local/share（packages/plugin/src/shared/data-path.ts），Windows 上
// 也是 .local\share 而不是 %APPDATA%。
const (
	storeDir  = "cortexkit"
	pluginDir = "magic-context"
	dbName    = "context.db"
	// legacyDir 是 OpenCode 时代的旧位置，插件 v0.16 之前装在那儿。
	legacyDir = "opencode"
)

// queryTimeout 与 dashboard 的 busy_timeout 5000 对齐：库处于 WAL 模式、
// 插件可能正在写，只读连接也要能等到写锁释放。
const queryTimeout = 5 * time.Second

// maxRows 是单次查询的行数上限。记忆表有几百行，一次全取会把片段响应
// 撑得过大；分页由调用方传 offset。
const maxRows = 200

// MaxPageRows 是单页行数的硬上限，供传输层校验入参。
const MaxPageRows = maxRows

// ErrUnavailable 表示这台机器上读不到 magic-context 的存储。
// 调用方应把它呈现成一句可读的说明，而不是一个错误码。
var ErrUnavailable = errors.New("magic_context_unavailable")

// Store 是 magic-context 本地库的只读视图。
type Store struct {
	// sqlite3 的绝对路径；为空表示本机没有这个可执行文件。
	binary string
	// dbPath 是解析出来的库路径；为空表示库不存在。
	dbPath string
	// baseDir 是库所在目录，用于展示来源。
	baseDir string

	mu      sync.Mutex
	checked bool
}

// NewStore 构造只读视图。此时不做任何磁盘访问，路径在第一次查询时惰性解析。
func NewStore() *Store {
	return &Store{}
}

// resolve 惰性解析库路径与 sqlite3 可执行文件。
//
// 只有在**找到库**时才锁定解析结果：插件首次运行前库还不存在，若这时就
// 锁定，装好/首次运行之后必须重启桥才看得见——而界面文案说的是「等它
// 首次运行后再看」。找不到就保持未锁定，下次查询重试。
func (s *Store) resolve() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checked {
		return
	}
	if path, err := exec.LookPath("sqlite3"); err == nil {
		s.binary = path
	}
	dbPath, baseDir := findDB()
	if dbPath == "" {
		return
	}
	s.dbPath, s.baseDir = dbPath, baseDir
	s.checked = true
}

// findDB 按 dashboard 同规则找库：先看显式的 MAGIC_CONTEXT_STORAGE_DIR，
// 再找跨 harness 的共享位置，最后回落到 OpenCode 时代的旧位置。
//
// 显式覆盖只接受绝对路径——相对路径在不同工作目录下会指向不同的库，
// 静默读错比明确失败更糟。
func findDB() (path, base string) {
	if raw := strings.TrimSpace(os.Getenv("MAGIC_CONTEXT_STORAGE_DIR")); raw != "" {
		explicit := filepath.Join(raw, dbName)
		if filepath.IsAbs(raw) {
			if fileExists(explicit) {
				return explicit, raw
			}
		}
		return "", ""
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	shared := filepath.Join(data, storeDir, pluginDir, dbName)
	if fileExists(shared) {
		return shared, filepath.Dir(shared)
	}
	legacy := filepath.Join(data, legacyDir, "storage", "plugin", pluginDir, dbName)
	if fileExists(legacy) {
		return legacy, filepath.Dir(legacy)
	}
	return "", ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Status 是面板头部的概况。
type Status struct {
	// Available 为真时下面几个计数才有意义。
	Available bool
	// Reason 在 Available 为假时说明原因，供界面直接展示。
	Reason string
	// Source 是库所在目录，让用户知道数据来自哪里。
	Source string
	// SizeBytes 是库文件大小。
	SizeBytes int64
	// Counts 是各表的行数。
	Counts map[string]int
	// Harness 是库记录的 harness 分布，例如 {"pi":89,"opencode":3}。
	Harness map[string]int
}

// tables 是面板要展示的表。顺序即界面上的顺序。
var tables = []struct {
	Key   string
	Label string
	Where string
}{
	{"memories", "记忆", "WHERE status='active'"},
	{"compartments", "会话分段", ""},
	{"user_memories", "用户指令", ""},
	{"notes", "笔记", ""},
	{"dream_runs", "Dreamer", ""},
}

// Status 读一次概况。库不存在或缺 sqlite3 时返回 Available=false。
func (s *Store) Status(ctx context.Context) Status {
	s.resolve()
	if s.binary == "" {
		return Status{Reason: "本机没有 sqlite3 可执行文件，无法读取 magic-context 的本地存储。"}
	}
	if s.dbPath == "" {
		return Status{Reason: "未检测到 magic-context 的本地存储（context.db）。若已安装该扩展，等待它首次运行后再看。"}
	}
	status := Status{Available: true, Source: s.baseDir, Counts: map[string]int{}}
	if info, err := os.Stat(s.dbPath); err == nil {
		status.SizeBytes = info.Size()
	}
	for _, table := range tables {
		rows, err := s.query(ctx, fmt.Sprintf("SELECT COUNT(*) AS n FROM %s %s", table.Key, table.Where))
		if err != nil {
			// 单表失败不该让整个概况失败：库可能正在迁移，某张表暂时不存在。
			status.Counts[table.Key] = -1
			continue
		}
		status.Counts[table.Key] = int(number(rows[0]["n"]))
	}
	// harness 分布说明这个库是跨 harness 共用的（OpenCode 与 Pi 各写各的会话）。
	if rows, err := s.query(ctx, "SELECT harness, COUNT(*) AS n FROM session_meta GROUP BY harness ORDER BY n DESC LIMIT 8"); err == nil {
		status.Harness = map[string]int{}
		for _, row := range rows {
			status.Harness[text(row["harness"])] = int(number(row["n"]))
		}
	}
	return status
}

// arg 是一个已经校验过的查询参数。
//
// sqlite3 的命令行**不支持位置参数**：数据库名之后每一个 argv 都会被当成
// 一条独立 SQL 依次执行（第一版就是这么写的，结果 offset/limit 被当成两条
// 语句执行，报 datatype mismatch）。所以这里不是「绑定参数」，而是把校验过
// 的值代回语句里。安全性因此完全依赖校验：字符串参数必须先过
// validateIdentifier / validateProjectKey。
type arg struct {
	isNum bool
	num   int64
	str   string
}

func intArg(v int) arg    { return arg{isNum: true, num: int64(v)} }
func strArg(v string) arg { return arg{str: v} }

// validateIdentifier 校验分类名这类「标识符」：只允许大写字母、数字与下划线。
// magic-context 的分类正是这种形态（CONSTRAINTS / PROJECT_RULES / NAMING）。
func validateIdentifier(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// validateProjectKey 校验项目键。库里的形态是 "dir:<16 位十六进制>"
// （按目录哈希）或 "git:<40 位十六进制>"（按仓库）。
func validateProjectKey(v string) bool {
	prefix, hash, ok := strings.Cut(v, ":")
	if !ok || hash == "" {
		return false
	}
	switch prefix {
	case "dir":
		// 目录哈希是 12 位十六进制（如 dir:0123abcd4567）。
		if len(hash) != 12 {
			return false
		}
	case "git":
		if len(hash) != 40 {
			return false
		}
	default:
		return false
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// render 把参数代回语句。数字直接写，字符串加单引号并双写内部引号。
func (a arg) render() string {
	if a.isNum {
		return strconv.FormatInt(a.num, 10)
	}
	return "'" + strings.ReplaceAll(a.str, "'", "''") + "'"
}

// query 执行一条写死的查询并返回 JSON 行。
//
// 只用 -readonly 打开，并用 .timeout 5000 与 context 双重约束：
// 库在 WAL 模式下可能被插件长时间持写锁，桥不能陪着一起卡住。
func (s *Store) query(ctx context.Context, sql string, args ...arg) ([]map[string]any, error) {
	if s.binary == "" || s.dbPath == "" {
		return nil, ErrUnavailable
	}
	// 占位符数量必须与参数一致，否则说明本包有 bug：把 ? 留给 sqlite
	// 只会得到一个难懂的语法错误。
	if strings.Count(sql, "?") != len(args) {
		return nil, fmt.Errorf("查询参数数量不匹配：语句有 %d 个占位符，传入 %d 个", strings.Count(sql, "?"), len(args))
	}
	for _, a := range args {
		sql = strings.Replace(sql, "?", a.render(), 1)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	// #nosec G204 -- binary 来自 exec.LookPath；sql 由本包写死的常量与已校验参数组成。
	cmd := exec.CommandContext(ctx, s.binary, "-readonly", "-batch", "-cmd", ".timeout 5000", "-json", s.dbPath, sql)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("读取 magic-context 存储超时：%w", ctx.Err())
		}
		return nil, fmt.Errorf("读取 magic-context 存储失败：%w（%s）", err, strings.TrimSpace(stderr.String()))
	}
	trimmed := bytes.TrimSpace(stdout.Bytes())
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, fmt.Errorf("解析 magic-context 存储结果失败：%w", err)
	}
	return rows, nil
}

func number(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func text(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
