package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func Test记录与查询(t *testing.T) {
	dir := t.TempDir()
	r, err := NewReceipts(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Record(Receipt{RequestID: "r1", Method: "session.prompt", Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	rec, ok := r.Lookup("r1")
	if !ok || rec.Outcome != OutcomeOK || rec.Method != "session.prompt" {
		t.Fatalf("回执查询异常: %+v %v", rec, ok)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("不存在的回执不应命中")
	}
}

func Test重启后仍可查询(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLimits()
	r, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = r.Record(Receipt{RequestID: "r" + string(rune('a'+i)), Method: "session.prompt", Outcome: OutcomeOK, At: time.Now()})
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if _, ok := r2.Lookup("ra"); !ok {
		t.Fatal("重启后回执丢失")
	}
	if r2.Stats()["degraded"].(bool) {
		t.Fatalf("重启后不应处于降级状态: %v", r2.Stats())
	}
}

func Test条数上限不无限增长(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLimits()
	limits.MaxEntries = 10
	r, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 100; i++ {
		_ = r.Record(Receipt{RequestID: "r" + strconv.Itoa(i), Method: "session.prompt", Outcome: OutcomeOK})
	}
	if got := r.Stats()["entries"].(int); got != 10 {
		t.Fatalf("内存回执数应受上限约束: %d", got)
	}
	if _, ok := r.Lookup("r0"); ok {
		t.Fatal("最旧的回执应被淘汰")
	}
	if _, ok := r.Lookup("r99"); !ok {
		t.Fatal("最新回执应保留")
	}
}

func Test轮转后旧文件被淘汰(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLimits()
	limits.MaxFileBytes = 2048
	limits.MaxRotated = 2
	r, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 200; i++ {
		_ = r.Record(Receipt{RequestID: "req-" + strconv.Itoa(i), Method: "session.prompt", Outcome: OutcomeUnknown, At: time.Now()})
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// 轮转文件数 + 当前文件，总数不应超过 MaxRotated+1。
	if len(files) > limits.MaxRotated+1 {
		t.Fatalf("日志文件总数超过上限: %v", files)
	}
}

func Test目录损坏时降级而不崩溃(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "corrupt.jsonl")
	if err := os.WriteFile(bad, []byte("不是 JSON\n{\"requestId\":\"ok1\",\"method\":\"m\",\"outcome\":\"ok\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := NewReceipts(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, ok := r.Lookup("ok1"); !ok {
		t.Fatal("损坏行之后的有效回执仍应加载")
	}
}

// Test重启加载保留较新的轮转回执 覆盖 B47：轮转文件按序号升序载入。
// 旧实现按 .3/.2/.1 倒序，条数预算耗尽在最旧文件上，receipts.1 读不进来。
// 这里直接构造日志文件，精确控制「新记录在 .1、旧记录在 .2」的布局，
// 不依赖轮转时序，否则夹具可能根本没走到目标路径。
func Test重启加载保留较新的轮转回执(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{MaxEntries: 4, MaxFileBytes: 1 << 20, MaxRotated: 3, LineBytes: 64 << 10}
	base := time.Now()
	write := func(name string, ids ...string) {
		t.Helper()
		var b strings.Builder
		for _, id := range ids {
			body, err := json.Marshal(Receipt{RequestID: id, Method: "session.prompt", Outcome: OutcomeOK, At: base})
			if err != nil {
				t.Fatal(err)
			}
			b.Write(body)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 旧记录在序号更大的文件里；新记录在 receipts.1。
	write("receipts.2.jsonl", "old-1", "old-2", "old-3", "old-4")
	write("receipts.1.jsonl", "new-a", "new-b", "new-c")
	r, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, id := range []string{"new-a", "new-b", "new-c"} {
		if _, ok := r.Lookup(id); !ok {
			t.Fatalf("较新的回执 %q 在重启后丢失", id)
		}
	}
	if entries := r.Stats()["entries"].(int); entries != 4 {
		t.Fatalf("应按条数上限保留 4 条，实际 %d", entries)
	}
}

// Test半行尾部追加后仍可读 覆盖 B58：崩溃半行必须先截回最后一个换行，
// 否则新回执接在坏 JSON 之后永远解析失败。
func Test半行尾部追加后仍可读(t *testing.T) {
	dir := t.TempDir()
	limits := DefaultLimits()
	r, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Receipt{RequestID: "complete", Method: "session.prompt", Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// 模拟写入中途崩溃：文件末尾留下没有换行的半行。
	path := filepath.Join(dir, "receipts.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"requestId":"half","method":"session`)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Record(Receipt{RequestID: "after-crash", Method: "session.prompt", Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	final, err := NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	if _, ok := final.Lookup("after-crash"); !ok {
		t.Fatal("崩溃后追加的回执无法重新加载")
	}
	if _, ok := final.Lookup("complete"); !ok {
		t.Fatal("崩溃前的完整回执丢失")
	}
	// 文件里不应残留半行与新记录粘连的行。
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var rec Receipt
		if json.Unmarshal([]byte(line), &rec) != nil {
			t.Fatalf("日志存在不可解析的行: %q", line)
		}
	}
}

// Test同一请求只保留最新结论 覆盖 put 的覆盖方向：
// pending→ok 之后，旧时间戳的记录不能把结论改回去。
func Test同一请求只保留最新结论(t *testing.T) {
	dir := t.TempDir()
	r, err := NewReceipts(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	base := time.Now()
	if err := r.Record(Receipt{RequestID: "req-1", Method: "session.prompt", Outcome: OutcomePending, At: base}); err != nil {
		t.Fatal(err)
	}
	// 故意用更早的时间戳写终态，模拟乱序到达。
	if err := r.Record(Receipt{RequestID: "req-1", Method: "session.prompt", Outcome: OutcomeOK, At: base.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	rec, ok := r.Lookup("req-1")
	if !ok || rec.Outcome != OutcomePending {
		t.Fatalf("旧时间戳覆盖了新结论: %+v", rec)
	}
	if err := r.Record(Receipt{RequestID: "req-1", Method: "session.prompt", Outcome: OutcomeOK, At: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	rec, _ = r.Lookup("req-1")
	if rec.Outcome != OutcomeOK {
		t.Fatalf("更新的终态未被接受: %+v", rec)
	}
}
