package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Test正文损坏的完整记录被拒绝 覆盖 B13：
// 快路径只读行首三字段，以前会把「字段齐全但正文写坏」的记录静默接受，
// 与「完整损坏行显式报错」的契约不符。
func Test正文损坏的完整记录被拒绝(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	path := filepath.Join(sessionDir, "broken.jsonl")
	header := `{"type":"session","version":3,"id":"broken","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`
	// 关键：损坏的记录必须不在返回页里。读取路径有 json.Valid 兜底，
	// 但它只校验被选中的条目；扫描阶段放进索引却不校验，坏记录就永远看不见。
	// 所以这里让 m1 损坏、m2 完好，只取一页一条——选中的只有 m2。
	bad := `{"type":"message","id":"m1","parentId":null,"timestamp":"t","message":{"role":"user","content":"hi"}`
	good := `{"type":"message","id":"m2","parentId":"m1","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`
	if err := os.WriteFile(path, []byte(header+"\n"+bad+"\n"+good+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, "broken", "", "", 1); err == nil {
		t.Fatal("扫描阶段必须拒绝正文损坏的记录，即使它不在本页里")
	}
}

// Test合法记录不被配平校验误伤 覆盖反向边界：
// 含转义引号、嵌套对象与数组的正常记录必须照常通过。
func Test合法记录不被配平校验误伤(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	path := filepath.Join(sessionDir, "ok.jsonl")
	header := `{"type":"session","version":3,"id":"ok","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`
	// 字符串里带转义引号、反斜杠和括号字符，最容易骗过朴素扫描。
	tricky := `{"type":"message","id":"m1","parentId":null,"timestamp":"t","message":{"role":"user","content":"他说 \"hello {world} [x] \\ 然后继续"}}`
	if err := os.WriteFile(path, []byte(header+"\n"+tricky+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	page, err := store.History(ctx, "ok", "", "", 50)
	if err != nil {
		t.Fatalf("合法记录被误判: %v", err)
	}
	if page.LeafID != "m1" {
		t.Fatalf("叶子异常: %s", page.LeafID)
	}
}
