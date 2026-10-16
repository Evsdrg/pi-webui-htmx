package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLog(t *testing.T, dir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, workerLogName))
	if err != nil {
		t.Fatalf("读取工作进程日志失败: %v", err)
	}
	return string(body)
}

// 工作进程的 stderr 是「Pi 侧到底报了什么」的唯一出口；此前它被指向
// /dev/null，进程级报错因此不进任何日志。这里钉住它确实落盘、且带 worker 标签。
func TestWorkerLog记录工作进程stderr(t *testing.T) {
	dir := t.TempDir()
	l := NewWorkerLog(dir, DefaultLogLimits())
	w := l.Writer("ab12cd34")
	if _, err := io.WriteString(w, "第一行\n第二行\n"); err != nil {
		t.Fatalf("写入不应报错: %v", err)
	}
	_ = w.Close()
	body := readLog(t, dir)
	if !strings.Contains(body, "第一行") || !strings.Contains(body, "第二行") {
		t.Fatalf("两行都应在日志中: %q", body)
	}
	if !strings.Contains(body, "[ab12cd34]") {
		t.Fatalf("每行应带 worker 标签以便分辨并发进程: %q", body)
	}
}

// stderr 的写入是分块的，可能在任何位置切开；没有换行结尾的尾串也必须
// 在关闭时补齐，否则报错的最后一行会被吞掉（那往往正是关键信息）。
func TestWorkerLog无换行结尾在关闭时补齐(t *testing.T) {
	dir := t.TempDir()
	l := NewWorkerLog(dir, DefaultLogLimits())
	w := l.Writer("t")
	_, _ = io.WriteString(w, "没有换行结尾")
	if strings.Contains(readLog(t, dir), "没有换行结尾") {
		t.Fatal("关闭前不应写入不完整的行")
	}
	_ = w.Close()
	if !strings.Contains(readLog(t, dir), "没有换行结尾") {
		t.Fatal("关闭时应补齐尾串")
	}
}

// 单行必须截断：一次栈回溯或一张被打印的 base64 可以把日志撑到任意大。
func TestWorkerLog单行超长被截断(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLogLimits()
	limits.MaxLineBytes = 128
	l := NewWorkerLog(dir, limits)
	w := l.Writer("x")
	_, _ = io.WriteString(w, strings.Repeat("a", 5000)+"\n")
	_ = w.Close()
	body := readLog(t, dir)
	if !strings.Contains(body, "截断") {
		t.Fatalf("超长行应带截断标记: %q", body)
	}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		// 允许截断标记本身的余量。
		if len(line) > int(limits.MaxLineBytes)+64 {
			t.Fatalf("行未被有效截断（%d 字节）: %q", len(line), line)
		}
	}
	if len(body) > 4096 {
		t.Fatalf("单行超长却写入了 %d 字节，截断无效", len(body))
	}
}

// 不带换行的巨量输出不能先把内存撑爆再截断：stderr 被逐块写入时，
// 若没有换行边界就一直攒着，截断就来不及了。这里钉住「攒到上限即落盘」。
func TestWorkerLog无换行的巨量输出不会撑爆内存(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLogLimits()
	limits.MaxLineBytes = 256
	limits.MaxFileBytes = 1 << 20
	l := NewWorkerLog(dir, limits)
	w := l.Writer("x")
	// 分块写入 1 MiB，全程没有换行。
	chunk := strings.Repeat("z", 4096)
	for i := 0; i < 256; i++ {
		if _, err := io.WriteString(w, chunk); err != nil {
			t.Fatalf("写入不应报错: %v", err)
		}
	}
	// 关键断言：缓冲不能随输入无限增长——落盘必须已经发生。
	if got := readLog(t, dir); got == "" {
		t.Fatal("达到上限后应已落盘，而不是一直攒在内存里")
	}
	_ = w.Close()
	body := readLog(t, dir)
	if !strings.Contains(body, "已截断") {
		t.Fatalf("超长未换行输出应被截断: %q", body[:min(len(body), 200)])
	}
}

func TestWorkerLog按体积轮转并保留限量(t *testing.T) {
	dir := t.TempDir()
	limits := LogLimits{MaxFileBytes: 400, MaxRotated: 2, MaxLineBytes: 200}
	l := NewWorkerLog(dir, limits)
	w := l.Writer("t")
	payload := strings.Repeat("B", 80)
	for i := 0; i < 40; i++ {
		_, _ = io.WriteString(w, payload+"\n")
	}
	_ = w.Close()

	if _, err := os.Stat(filepath.Join(dir, workerLogName)); err != nil {
		t.Fatalf("当前文件应存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "worker.1.log")); err != nil {
		t.Fatalf("应发生轮转，worker.1.log 应存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "worker.3.log")); err == nil {
		t.Fatal("超出 MaxRotated 的轮转文件不应存在（总量必须封顶）")
	}
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil {
			total += info.Size()
		}
	}
	// 现行文件上限 + 保留的轮转文件上限。
	ceiling := int64(limits.MaxFileBytes) * int64(limits.MaxRotated+1)
	if total > ceiling {
		t.Fatalf("日志总量 %d 超过上限 %d", total, ceiling)
	}
}

// 日志目录建不出来时只能降级，绝不能 panic 或让调用方出错——日志是尽力而为的
// 副作用，不能反过来拖垮会话。（注意：删掉目录不算「写不进去」——在 Linux 上
// 已打开的 fd 仍然有效，所以这里用「目录位置的父路径是个文件」来构造真的失败。）
func TestWorkerLog目录不可用时降级(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// blocker 是文件，它下面建不出目录。
	l := NewWorkerLog(filepath.Join(blocker, "logs"), DefaultLogLimits())
	if !l.Degraded() {
		t.Fatal("目录建不出来时应标记为降级")
	}
	w := l.Writer("t")
	if _, err := io.WriteString(w, "写一次\n"); err != nil {
		t.Fatalf("降级后写入不应向 Pi 的 stderr 管道报错: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("降级后关闭不应报错: %v", err)
	}
}

// 正常目录下写入不该出现降级标记（防止把「降级」写成恒真）。
func TestWorkerLog正常写入不降级(t *testing.T) {
	dir := t.TempDir()
	l := NewWorkerLog(dir, DefaultLogLimits())
	if l.Degraded() {
		t.Fatal("目录可建时不应处于降级态")
	}
	w := l.Writer("t")
	_, _ = io.WriteString(w, "写一次\n")
	if l.Degraded() {
		t.Fatal("正常写入后不应处于降级态")
	}
	_ = w.Close()
}

// 端到端：真实 spawn 出的假 Pi 写的 stderr 必须落到日志文件里。
func Test工作进程stderr落入日志文件(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_PI_STDERR", "启动告警：示例\n")
	m, cwd := newTestManager(t, func(c *Config) {
		c.WorkerLog = NewWorkerLog(dir, DefaultLogLimits())
	})
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if w.Info().SessionID == "" {
		t.Fatal("握手未完成")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		body, err := os.ReadFile(filepath.Join(dir, workerLogName))
		if err == nil && strings.Contains(string(body), "启动告警：示例") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("假 Pi 的 stderr 未落入日志文件（快照: %q, err=%v）", string(body), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
