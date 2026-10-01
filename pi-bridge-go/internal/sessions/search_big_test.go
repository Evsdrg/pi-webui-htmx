package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 大会话必须能被搜到。
//
// 反例是实测出来的：MaxFileBytes 原本是 16 MiB，而本机最大的会话 86.9 MB
// ——它在列表里打得开、在历史里看得见，搜索却完全返回 0 条命中，且只标记
// truncated，不提「这个文件被整个跳过了」。同一个文件不该在「能不能看」
// 和「能不能搜」上得到两个答案，所以单文件上限与会话的 FileBytes 对齐。
func Test大于搜索单文件上限的会话仍可搜到(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	const keyword = "需要在大会话里被搜到的词"
	id := writeBigSession(t, dir, "huge", cwd, 20<<20, keyword)
	if got := statSize(t, filepath.Join(dir, id+".jsonl")); got <= 16<<20 {
		t.Fatalf("用例前提不成立：文件只有 %d 字节，应大于 16 MiB", got)
	}

	out, err := store.Search(context.Background(), keyword, DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) == 0 {
		t.Fatal("大会话里的内容必须能被搜到（默认限额下命中为 0）")
	}
	if out.Matches[0].SessionID != id {
		t.Fatalf("命中应属于大会话，实际 %s", out.Matches[0].SessionID)
	}
}

// 总字节预算兜底：单文件上限放开后，MaxFiles × 单文件 的理论最坏会到几十 GB。
// 罕见词必须读完所有候选（命中数不会让它提前停），所以按总量拦一道。
func Test搜索总字节预算封顶(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	// 两个各约 3 MiB 的会话；预算设成只够读一个。
	writeBigSession(t, dir, "s1", cwd, 3<<20, "甲")
	writeBigSession(t, dir, "s2", cwd, 3<<20, "乙")

	limits := DefaultSearchLimits()
	limits.MaxTotalBytes = 4 << 20
	out, err := store.Search(context.Background(), "绝不会出现的词zzz", limits)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("超过总字节预算应标记 truncated，不能宣称搜完了")
	}
	if out.Scanned > 1 {
		t.Fatalf("预算只够读一个文件，实际扫过 %d 个", out.Scanned)
	}
}

// 反向：预算足够时不截断，两个文件都扫。
func Test总字节预算够时不截断(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeBigSession(t, dir, "s1", cwd, 1<<20, "甲")
	writeBigSession(t, dir, "s2", cwd, 1<<20, "乙")

	limits := DefaultSearchLimits()
	limits.MaxTotalBytes = 64 << 20
	out, err := store.Search(context.Background(), "绝不会出现的词zzz", limits)
	if err != nil {
		t.Fatal(err)
	}
	if out.Scanned != 2 {
		t.Fatalf("预算足够时应扫过两个文件，实际 %d", out.Scanned)
	}
	if out.Truncated {
		t.Fatal("预算足够时不该标记截断")
	}
}

// writeBigSession 造一个至少 minBytes 的会话，末条带 keyword。
func writeBigSession(t *testing.T, dir, id, cwd string, minBytes int, keyword string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"session","version":3,"id":"%s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"%s"}`+"\n", id, cwd)
	pad := strings.Repeat("填充内容", 400) // 单条约 5 KB
	parent := "null"
	n := 0
	for b.Len() < minBytes {
		cur := fmt.Sprintf("p%d", n)
		fmt.Fprintf(&b, `{"type":"message","id":"%s","parentId":%s,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"%s"}}`+"\n", cur, parent, pad)
		parent = `"` + cur + `"`
		n++
	}
	fmt.Fprintf(&b, `{"type":"message","id":"last","parentId":%s,"timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"%s"}}`+"\n", parent, keyword)
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return id
}

func statSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
