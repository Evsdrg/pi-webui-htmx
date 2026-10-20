package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture 造一份 u1→a2→a3→a4→a5→a6 的会话，并返回只含指定 ID 的索引。
//
// 用「手工裁剪索引」而不是真的把窗口调小：窗口大小由文件体积决定，
// 构造一个 4 MiB 的会话只会让测试变慢，而 bug 与窗口怎么产生无关——
// 只要有「祖先不在索引里」这个形状就够了。
func windowFixture(t *testing.T, indexIDs ...string) (*os.File, map[string]node, int64) {
	t.Helper()
	cwd := t.TempDir()
	dir := t.TempDir()
	const id = "boundary"
	lines := []string{
		`{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`,
		entryRole("u1", "", "user"),
		entryRole("a2", "u1", "assistant"),
		entryRole("a3", "a2", "assistant"),
		entryRole("a4", "a3", "assistant"),
		entryRole("a5", "a4", "assistant"),
		entryRole("a6", "a5", "assistant"),
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	want := map[string]bool{}
	for _, entryID := range indexIDs {
		want[entryID] = true
	}
	nodes := map[string]node{}
	// 偏移是**文件内绝对位置**：会话头也占字节，跳过它时必须把它的长度算进去，
	// 否则按偏移读出来的是错位的字节，json.Valid 失败又会伪装成「文件已变化」。
	offset := int64(len(lines[0]) + 1)
	for _, line := range lines[1:] { // 跳过会话头
		size := int64(len(line) + 1)
		var rec struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			Message  struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if want[rec.ID] {
			nodes[rec.ID] = node{parent: rec.ParentID, offset: offset, size: int(size), isUser: rec.Message.Role == "user"}
		}
		offset += size
	}
	return f, nodes, offset
}

// 一页恰好填满到窗口最旧那条、而它不是 user 时的形状，是生产上真实撞到的
// 「读取期间历史文件发生变化」：
//
//	u1 → a2 → a3 → a4 → a5 → a6      （窗口只索引 a3..a6）
//
// limit=4 吃掉 a6..a3，轮边界对齐要再往前一步——那一步的 a2 不在索引里。
// 旧实现直接把零值 node 当成真条目追加进页（size=0、parent 为空），取页时
// 对空字节做 json.Valid 必然失败，于是「索引到不了那里」被报成了「文件被
// 改写」。正确语义是原样交回游标，让调用方退到全扫。
func Test窗口边界满页不报文件已变化(t *testing.T) {
	f, nodes, _ := windowFixture(t, "a3", "a4", "a5", "a6")
	s := &Store{limits: DefaultLimits()}
	page, ok, err := s.historyPage(context.Background(), f, "boundary", "a6", "", 4, nodes, "a6", false, false)
	if err != nil {
		t.Fatalf("祖先不在索引里是窗口边界，不该报「文件发生变化」: %v", err)
	}
	if ok {
		t.Fatalf("窗口不够用应返回 ok=false 交给全扫，实际返回了 %d 条", len(page.Entries))
	}
}

// 同样的边界在封顶索引上必须干净收页并标记「更早不可达」，同样不报错。
func Test封顶索引满页边界标记为不可达(t *testing.T) {
	f, nodes, _ := windowFixture(t, "a3", "a4", "a5", "a6")
	s := &Store{limits: DefaultLimits()}
	page, ok, err := s.historyPage(context.Background(), f, "boundary", "a6", "", 4, nodes, "a6", false, true)
	if err != nil {
		t.Fatalf("封顶边界应干净收页: %v", err)
	}
	if !ok {
		t.Fatal("封顶索引不该要求退全扫（全扫同样超上限，只会以错误告终）")
	}
	if len(page.Entries) != 4 {
		t.Fatalf("应返回索引内那 4 条，实际 %d 条", len(page.Entries))
	}
	if page.HasMore {
		t.Fatal("封顶边界之后没有可翻内容")
	}
	if !page.IndexCapped {
		t.Fatal("封顶边界必须标记 IndexCapped，才能与「真的没有更早历史」区分")
	}
}

// alignToTurn 只能追加索引里真的存在的条目；索引外的祖先原样交回游标。
func Test对齐不引入索引外的条目(t *testing.T) {
	s := &Store{limits: DefaultLimits()}
	nodes := map[string]node{"a3": {parent: "u2", size: 10}}
	selected, cursor := s.alignToTurn(nodes, []string{"a3"}, 10, "u2")
	if len(selected) != 1 || selected[0] != "a3" {
		t.Fatalf("不得把索引外的 u2 追加进页: %v", selected)
	}
	if cursor != "u2" {
		t.Fatalf("游标应原样停在不存在的祖先上，实际 %q", cursor)
	}
}

// 端到端：真实文件 + 真实尾扫窗口，一路翻到底必须全部成功。
//
// 上面三条测的是 historyPage 的边界处置；这一条防的是「窗口裁剪逻辑与
// 对齐逻辑再次配合出错」——它跑的是完整的 History，包括尾扫、缓存、
// 退全扫与轮边界对齐。生产上那次误报就发生在连续翻页的过程中。
func Test连续翻页不因窗口边界报错(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	// 造 400 条、每条约 12 KiB 的会话（约 4.8 MB），保证尾扫窗口装不下全部。
	lines := make([]string, 0, 400)
	prev := ""
	for i := 0; i < 400; i++ {
		id := "e" + itoa(i)
		role := "user"
		if i%3 != 0 {
			role = "assistant"
		}
		lines = append(lines, `{"type":"message","id":"`+id+`","parentId":`+jsonQuote(prev)+`,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"`+role+`","content":"`+strings.Repeat("x", 12<<10)+`"}}`)
		prev = id
	}
	id := writeSession(t, dir, "paging", cwd, lines...)
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	before := ""
	seen := 0
	for round := 0; round < 50; round++ {
		page, err := store.History(ctx, id, "", before, 7)
		if err != nil {
			t.Fatalf("第 %d 轮翻页失败（窗口边界不该报错）: %v", round+1, err)
		}
		seen += len(page.Entries)
		if !page.HasMore {
			break
		}
		before = page.OldestEntryID
	}
	if seen != 400 {
		t.Fatalf("应翻完全部 400 条，实际 %d 条", seen)
	}
}
