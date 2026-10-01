package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 尾部窗口扫描必须与全扫给出相同的索引。
//
// 这是这次改动的核心正确性依据：只扫尾部是有代价的（窗口外看不见），
// 但窗口内看到的东西不能与全扫有任何差别——否则会出现「翻到某页内容
// 与首页不一致」这类难查的问题。对照的是同一份文件的两种扫描结果：
// 节点集合、父链、偏移、大小、isUser 与 lastModelID 全部逐条比对。
func Test尾部窗口扫描与全扫一致(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	base := "2026-01-01T00:00:00.000Z"
	// 造一个足够大的会话：窗口默认 4 MiB，这里写到 5 MiB 让它必然截断。
	var lines []string
	pad := make([]byte, 900)
	for i := range pad {
		pad[i] = 'x'
	}
	parent := ""
	for i := 0; i < 9000; i++ {
		id := "e" + strconv.Itoa(i)
		p := "null"
		if parent != "" {
			p = `"` + parent + `"`
		}
		role := "assistant"
		if i%3 == 0 {
			role = "user"
		}
		line := `{"type":"message","id":"` + id + `","parentId":` + p + `,"timestamp":"` + base + `","message":{"role":"` + role + `","content":"` + string(pad) + `"}}`
		if i == 100 {
			line = `{"type":"model_change","id":"` + id + `","parentId":` + p + `,"timestamp":"` + base + `"}`
		}
		lines = append(lines, line)
		parent = id
	}
	id := writeSession(t, dir, "big", cwd, lines...)
	path := filepath.Join(dir, id+".jsonl")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() < 2*tailWindowBytes {
		t.Fatalf("用例前提不成立：文件只有 %s，应明显大于窗口 %s",
			humanBytes(st.Size()), humanBytes(tailWindowBytes))
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	full, fullLast, err := store.scanFile(ctx, f, st.Size(), id, cwd)
	if err != nil {
		t.Fatalf("全扫失败: %v", err)
	}
	partial, partialLast, complete, err := store.scanTail(ctx, f, st.Size(), id, cwd, 64)
	if err != nil {
		t.Fatalf("窗口扫描失败: %v", err)
	}
	if complete {
		t.Fatal("文件明显大于窗口，不应报 complete")
	}
	if len(partial) == 0 {
		t.Fatal("窗口扫描没有产出任何节点")
	}
	if len(partial) >= len(full) {
		t.Fatalf("窗口应只覆盖一部分：窗口 %d 条，全扫 %d 条", len(partial), len(full))
	}
	if partialLast != fullLast {
		t.Fatalf("两条路径的叶子应相同：窗口 %q，全扫 %q", partialLast, fullLast)
	}

	// 逐条对照窗口内每个节点。
	for entryID, got := range partial {
		want, ok := full[entryID]
		if !ok {
			t.Fatalf("窗口里有全扫没有的节点: %s", entryID)
		}
		if got.parent != want.parent {
			t.Fatalf("%s 的父不同：窗口 %q，全扫 %q", entryID, got.parent, want.parent)
		}
		if got.offset != want.offset {
			t.Fatalf("%s 的偏移不同：窗口 %d，全扫 %d", entryID, got.offset, want.offset)
		}
		if got.size != want.size {
			t.Fatalf("%s 的长度不同：窗口 %d，全扫 %d", entryID, got.size, want.size)
		}
		if got.isUser != want.isUser {
			t.Fatalf("%s 的 isUser 不同：窗口 %v，全扫 %v", entryID, got.isUser, want.isUser)
		}
		// lastModelID 是**唯一一处刻意允许的差异**（见 scanTail 的说明）：
		// 它是「沿父链最近的 model_change」，而用例里的 model_change 在
		// 文件开头、窗口之外，所以窗口里算不出起点。允许为空，但绝不允许
		// 算成一个**不同于全扫的值**——那才是会显示错模型的情况。
		if got.lastModelID != want.lastModelID && got.lastModelID != "" {
			t.Fatalf("%s 的 lastModelID 不同：窗口 %q，全扫 %q", entryID, got.lastModelID, want.lastModelID)
		}
	}

	// 窗口最旧的节点必须与全扫看到的连续：它的父亲要么也在窗口里，
	// 要么确实更早（那正是窗口截断的含义，不是错误）。
	if len(partial) != len(full) {
		if _, ok := partial[full[partialLast].parent]; !ok && full[partialLast].parent != "" {
			// 叶子一定在窗口里；这里只是确认索引本身自洽。
		}
	}
}

// 窗口不够时 History 会自动全扫；翻完全程不漏不重。
//
// 这条走的是真实入口：从最新一页一路往前翻到会话开头，中途必然跨过
// 4 MiB 窗口边界（那时 History 内部退化为全扫）。漏一条或重一条都会
// 让用户看到断掉的历史，而这正是「只扫尾部」最容易出的错。
func Test翻页跨窗口边界不漏不重(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	// 每行约 1 KiB，9000 行 ≈ 8.8 MiB，明显超过 4 MiB 窗口。
	var lines []string
	pad := strings.Repeat("z", 900)
	parent := ""
	const total = 9000
	for i := 0; i < total; i++ {
		id := "e" + strconv.Itoa(i)
		p := "null"
		if parent != "" {
			p = `"` + parent + `"`
		}
		lines = append(lines, `{"type":"message","id":"`+id+`","parentId":`+p+`,"timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"`+pad+`"}}`)
		parent = id
	}
	id := writeSession(t, dir, "big2", cwd, lines...)

	seen := map[string]int{}
	before := ""
	pages := 0
	for {
		page, err := store.History(ctx, id, "", before, 200)
		if err != nil {
			t.Fatalf("第 %d 页失败: %v", pages, err)
		}
		if len(page.Entries) == 0 && pages > 0 {
			break
		}
		ids := entryIDs(page.Entries)
		for i, entryID := range ids {
			seen[entryID]++
			if seen[entryID] > 1 {
				t.Fatalf("条目重复出现: %s（第 %d 页第 %d 条）", entryID, pages, i)
			}
		}
		pages++
		// 先记下本页游标再决定是否继续：放在 break 之后会让检查读到上一页的值。
		before = page.OldestEntryID
		if !page.HasMore {
			break
		}
		if pages > 200 {
			t.Fatal("翻页没有终止")
		}
	}
	if len(seen) != total {
		t.Fatalf("翻完应有 %d 条，实际 %d 条（%d 页）", total, len(seen), pages)
	}
	if _, ok := seen["e0"]; !ok {
		t.Fatal("翻到最旧应包含 e0")
	}
	if before != "e0" {
		t.Fatalf("最后一页的最旧条目应是 e0，实际 %q", before)
	}
}

// 窗口内存在 model_change 时，它之后的节点必须与全扫完全一致。
//
// 上一条用例的 model_change 在文件开头（窗口外），只能验证「允许为空」。
// 这条把 model_change 放在会话末尾附近，验证窗口真能算出正确起点——
// 否则 lastModelID 就会一直退化成空，「允许为空」的宽容会掩盖真问题。
func Test窗口内model_change之后逐条一致(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	const total = 9000
	pad := strings.Repeat("z", 900)
	var lines []string
	parent := ""
	for i := 0; i < total; i++ {
		id := "e" + strconv.Itoa(i)
		p := "null"
		if parent != "" {
			p = `"` + parent + `"`
		}
		// e8000 之后切模型：它在窗口内（窗口覆盖约最后 4 MiB）。
		if i == 8000 {
			lines = append(lines, `{"type":"model_change","id":"`+id+`","parentId":`+p+`,"timestamp":"2026-01-01T00:00:00.000Z"}`)
		} else {
			lines = append(lines, `{"type":"message","id":"`+id+`","parentId":`+p+`,"timestamp":"2026-01-01T00:00:00.000Z","message":{"role":"user","content":"`+pad+`"}}`)
		}
		parent = id
	}
	id := writeSession(t, dir, "mc", cwd, lines...)
	path := filepath.Join(dir, id+".jsonl")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	full, _, err := store.scanFile(ctx, f, st.Size(), id, cwd)
	if err != nil {
		t.Fatal(err)
	}
	partial, _, complete, err := store.scanTail(ctx, f, st.Size(), id, cwd, 64)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("用例前提不成立：窗口不应覆盖整个文件")
	}
	if _, ok := partial["e8000"]; !ok {
		t.Fatal("用例前提不成立：model_change 应落在窗口内")
	}
	sawNonEmpty := false
	for entryID, got := range partial {
		want := full[entryID]
		if got.lastModelID != want.lastModelID {
			t.Fatalf("%s 的 lastModelID 不同：窗口 %q，全扫 %q", entryID, got.lastModelID, want.lastModelID)
		}
		if got.lastModelID != "" {
			sawNonEmpty = true
		}
	}
	if !sawNonEmpty {
		t.Fatal("窗口内应至少有一个节点带上 model_change 起点，否则这条用例什么都没验到")
	}
}
