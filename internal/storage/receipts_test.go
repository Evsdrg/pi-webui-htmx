package storage

import (
	"os"
	"path/filepath"
	"strconv"
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
