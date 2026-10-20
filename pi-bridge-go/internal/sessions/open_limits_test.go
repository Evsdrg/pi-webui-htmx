package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 2026-10-21 重设计：打开会话**不该拒**，翻不动才该说。
//
// 旧设计在 History/lazy/metadata 三个尾部读取路径的入口检查 FileBytes，
// 超限直接拒绝。但这三条路径本来就是「倒着读」：尾扫 4 MiB 窗口起步、
// 按需扩到 32 MiB，与文件总体积无关；lazy 只读单条，metadata 只读首尾
// 片段。为「深翻页时可能发生的全扫」在门口把整条路径拒掉，代价是用户
// 完全看不到内容，换来的只是深翻页时才付的一次性成本（实测线性且便宜：
// 262 MB / 73119 条 = 393 ms）。
//
// 新契约：
//   - 尾部路径（History / lazy / metadata）不再检查 FileBytes；
//   - 全扫路径（tree / export / History 深翻页退化的全扫）保留天花板
//     （默认提到 2 GiB），超限仍在**那一步**报错；
//   - Entries 不再拒绝：尾扫按条数封顶，更早的历史标记为「不可达」。
func Test尾部路径不再因FileBytes拒开(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	// 造一个正常会话（约 1 KiB），把 FileBytes 压到远小于它。
	// a1 带一个 thinking 块，lazy 的 Thinking 路径才有东西可读。
	thinking := `{"type":"message","id":"a1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"想"},{"type":"text","text":"答"}]}}`
	id := writeSession(t, dir, "big-open", cwd,
		entryRole("u1", "", "user"),
		thinking,
		entryRole("u2", "a1", "user"),
	)
	limits := DefaultLimits()
	limits.FileBytes = 64 // 旧设计在这里拒开整条路径
	store, err := New(dir, mustPolicy(t, cwd), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.History(ctx, id, "", "", 10); err != nil {
		t.Fatalf("History 不应因 FileBytes 拒开（首页只读尾部窗口）: %v", err)
	}
	if got, err := store.Thinking(ctx, id, "a1", 0); err != nil || got != "想" {
		t.Fatalf("lazy 只读单条，不应因 FileBytes 拒绝: got=%q err=%v", got, err)
	}
}

// 全扫路径的天花板仍然生效：导出要顺序读全文件，超过天花板在那一步报错。
func Test全扫路径保留FileBytes天花板(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	id := writeSession(t, dir, "big-export", cwd,
		entryRole("u1", "", "user"),
		entryRole("a1", "u1", "assistant"),
	)
	limits := DefaultLimits()
	limits.FileBytes = 64
	store, err := New(dir, mustPolicy(t, cwd), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ExportDocument(context.Background(), id, DefaultExportLimits()); err == nil {
		t.Fatal("导出是全扫路径，超过天花板必须报错")
	}
}

// Entries 不再拒绝：尾扫按条数封顶，翻到边界时更早的历史标记「不可达」，
// 而不是整个会话报「历史条目数超过 N 条上限」。
func TestEntries封顶继续读(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	// 10 条记录的链：u1→a1→…→u5；Entries 压到 6（含尾部对齐的余量也要够不着）。
	lines := make([]string, 0, 10)
	lines = append(lines, entryRole("u1", "", "user"))
	for i := 1; i <= 9; i++ {
		lines = append(lines, entryRole(entryID(i+1), entryID(i), roleOf(i)))
	}
	id := writeSession(t, dir, "capped", cwd, lines...)
	limits := DefaultLimits()
	limits.Entries = 6
	store, err := New(dir, mustPolicy(t, cwd), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// 首页必须能开（旧设计直接报「历史条目数超过 6 条上限」）。
	page, err := store.History(ctx, id, "", "", 2)
	if err != nil {
		t.Fatalf("超 Entries 的会话必须能打开: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("首页不应为空")
	}
	// 一路向前翻，最终必须停在「不可达」上，而不是报错。
	reached := map[string]bool{}
	capped := false
	before := ""
	for round := 0; round < 20; round++ {
		page, err := store.History(ctx, id, "", before, 2)
		if err != nil {
			t.Fatalf("第 %d 轮翻页不应报错: %v", round+1, err)
		}
		for _, e := range page.Entries {
			var m struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(e, &m) == nil {
				reached[m.ID] = true
			}
		}
		if !page.HasMore {
			capped = page.IndexCapped
			break
		}
		before = page.OldestEntryID
	}
	if !capped {
		t.Fatalf("翻到边界应标记 IndexCapped，实际翻到 %d 条后 HasMore=false 且无标记: %v", len(reached), reached)
	}
	// 封顶边界之前（较新）的条目必须可达，边界之后（最旧的）不可达——
	// 尾扫从最新往回收，被截掉的一定是最旧的几条。链是 u1→e2→…→e10，
	// 封 6 条后可达集应为 e5..e10，u1..e4 不可达。
	if !reached["e5"] {
		t.Fatalf("封顶后最新的 6 条应可达: %v", reached)
	}
	if reached["e4"] || reached["u1"] {
		t.Fatalf("超出封顶的最旧条目不应可达: %v", reached)
	}
}

func entryID(n int) string { return "e" + strings.Repeat("x", 0) + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func roleOf(i int) string {
	// 交替 user/assistant，让翻页边界落在轮中间也能对齐。
	if i%2 == 1 {
		return "assistant"
	}
	return "user"
}
