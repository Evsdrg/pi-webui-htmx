package sessions

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestSearch命中数上限被夹紧 覆盖 B72：
// 客户端 limit 曾被直接写进 MaxMatches，没有上限——limit=1e9 会把整个
// 语料的命中全部返回。上限必须由搜索能力自己兜住，而不是指望调用方自觉。
func TestSearch命中数上限被夹紧(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	const rows = MaxMatchesLimit + 100
	var b strings.Builder
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, `{"type":"message","id":"u%d","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"命中关键词 %d"}}`+"\n", i, i)
	}
	writeSearchSession(t, dir, cwd, "s1", b.String())

	limits := DefaultSearchLimits()
	limits.MaxMatches = 1_000_000_000
	out, err := store.Search(context.Background(), "命中关键词", limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) > MaxMatchesLimit {
		t.Fatalf("命中数 %d 超过上限 %d", len(out.Matches), MaxMatchesLimit)
	}
	// 语料比上限大，所以应当恰好顶到上限：夹紧过头会把合法的大页搜索砍掉。
	if len(out.Matches) != MaxMatchesLimit {
		t.Fatalf("应返回上限 %d 条，得到 %d", MaxMatchesLimit, len(out.Matches))
	}

	// 小于上限的请求保持原样，夹紧不能把小的放大。
	limits.MaxMatches = 10
	small, err := store.Search(context.Background(), "命中关键词", limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(small.Matches) != 10 {
		t.Fatalf("limit=10 应返回 10 条，得到 %d", len(small.Matches))
	}
}
