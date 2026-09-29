package transport

import (
	"os"
	"path/filepath"
	"testing"

	"pi-bridge-go/internal/observe"
	"pi-bridge-go/internal/storage"
)

// 回执写失败不能静默：它不影响命令结果，但会让重启后的对账失去依据。
// 这里用「轮转失败」造一个真实写错误（目录被删掉，rename 必然失败）。
func Test回执写失败被计入指标(t *testing.T) {
	dir := t.TempDir()
	limits := storage.DefaultLimits()
	limits.MaxFileBytes = 1 // 第一条就触发轮转
	receipts, err := storage.NewReceipts(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	metrics := observe.NewMetrics(nil)
	s := &Server{receipts: receipts, metrics: metrics, claims: newClaims(16)}
	s.storeReceipt(storage.Receipt{RequestID: "r1", Method: "session.prompt", Outcome: storage.OutcomeOK})
	before := metrics.Snapshot()["receiptFailures"].(uint64)

	if err := os.RemoveAll(filepath.Join(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	s.storeReceipt(storage.Receipt{RequestID: "r2", Method: "session.prompt", Outcome: storage.OutcomeOK})
	after := metrics.Snapshot()["receiptFailures"].(uint64)
	if after <= before {
		t.Fatalf("写失败必须计入 receiptFailures: before=%d after=%d", before, after)
	}
}

// 「没有启用回执存储」是配置事实，不是故障：关闭后的写入不该被算成失败。
func Test未启用回执不计入失败(t *testing.T) {
	receipts, err := storage.NewReceipts(t.TempDir(), storage.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receipts.Close(); err != nil {
		t.Fatal(err)
	}
	metrics := observe.NewMetrics(nil)
	s := &Server{receipts: receipts, metrics: metrics, claims: newClaims(16)}
	s.storeReceipt(storage.Receipt{RequestID: "r1", Method: "session.prompt", Outcome: storage.OutcomeOK})
	if got := metrics.Snapshot()["receiptFailures"].(uint64); got != 0 {
		t.Fatalf("ErrNotEnabled 不应计入失败，得到 %d", got)
	}
}

// metrics 为 nil 时（测试与裁剪部署）必须退化为空操作，不能 panic。
func Test回执统计在无指标时安全(t *testing.T) {
	dir := t.TempDir()
	receipts, err := storage.NewReceipts(dir, storage.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	s := &Server{receipts: receipts, claims: newClaims(16)}
	s.storeReceipt(storage.Receipt{RequestID: "r1", Method: "session.prompt", Outcome: storage.OutcomeOK})
	if _, ok := receipts.Lookup("r1"); !ok {
		t.Fatal("无指标时仍应正常落盘")
	}
}
