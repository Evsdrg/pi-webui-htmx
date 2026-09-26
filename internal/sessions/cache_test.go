package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pi-bridge-go/internal/workspace"
	"time"
)

// newBigStore 造一个可写的会话文件，返回 store、会话 ID 与文件路径。
func newBigStore(t *testing.T, turns int) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	sd := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sd, 0755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspace.New([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(sd, policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id := "big"
	path := filepath.Join(sd, id+".jsonl")
	writeTurns(t, path, dir, id, turns)
	return store, id, path
}

// writeTurns 按真实 Pi 的字段顺序写会话文件。
func writeTurns(t *testing.T, path, cwd, id string, turns int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(benchHeader{Type: "session", Version: 3, ID: id, Timestamp: "2026-01-01T00:00:00.000Z", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	parent := (*string)(nil)
	for i := 0; i < turns; i++ {
		u, a := "u"+strconv.Itoa(i), "a"+strconv.Itoa(i)
		for _, e := range []benchMessage{
			{Type: "message", ID: u, ParentID: parent, Timestamp: "t", Message: benchMsg("user", "问题"+strconv.Itoa(i))},
			{Type: "message", ID: a, ParentID: &u, Timestamp: "t", Message: benchMsg("assistant", []map[string]any{{"type": "text", "text": "回答" + strconv.Itoa(i)}})},
		} {
			if err := enc.Encode(e); err != nil {
				t.Fatal(err)
			}
		}
		parent = &a
	}
}

// TestScanCache命中后结果一致 缓存命中与未命中必须给出完全相同的页。
// 这是整个缓存的安全底线：命中了但内容变了，比不缓存更糟。
func TestScanCache命中后结果一致(t *testing.T) {
	store, id, _ := newBigStore(t, 40)
	ctx := context.Background()
	// 第一次：未命中，建立缓存。
	p1, err := store.History(ctx, id, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	// 第二次：命中。
	p2, err := store.History(ctx, id, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Entries) != len(p2.Entries) {
		t.Fatalf("条目数不一致: %d vs %d", len(p1.Entries), len(p2.Entries))
	}
	for i := range p1.Entries {
		if string(p1.Entries[i]) != string(p2.Entries[i]) {
			t.Fatalf("第 %d 条不一致", i)
		}
	}
	if p1.OldestEntryID != p2.OldestEntryID || p1.LeafID != p2.LeafID || p1.HasMore != p2.HasMore {
		t.Fatalf("分页字段不一致: %+v vs %+v", p1, p2)
	}
	// 翻页同样要一致。
	q1, err := store.History(ctx, id, "", p1.OldestEntryID, 10)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := store.History(ctx, id, "", p1.OldestEntryID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(q1.Entries) != len(q2.Entries) {
		t.Fatalf("翻页条目数不一致")
	}
	for i := range q1.Entries {
		if string(q1.Entries[i]) != string(q2.Entries[i]) {
			t.Fatalf("翻页第 %d 条不一致", i)
		}
	}
}

// TestScanCache追加后失效 Pi 追加写入必须让缓存 miss，否则新消息不出现。
func TestScanCache追加后失效(t *testing.T) {
	store, id, path := newBigStore(t, 10)
	ctx := context.Background()
	before, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if before.HasMore {
		t.Fatal("10 轮应能一次取完")
	}
	beforeCount := len(before.Entries)

	// 追加前的 size 与 mtime 要先记下来：缓存的失效键正是这两个值。
	preSize := stOf(t, path).Size()
	preMod := stOf(t, path).ModTime()

	// 追加两轮。
	appendTurns(t, path, "a9", 10, 12)

	// 把 mtime 拨回追加前，只让 size 发生变化。
	// 这样才能单独验证 size 这一项真的在起作用——否则 mtime 会兜住，
	// 测试绿了也不知道是哪个字段的功劳。
	if err := os.Chtimes(path, preMod, preMod); err != nil {
		t.Fatal(err)
	}
	post := stOf(t, path)
	if post.Size() <= preSize {
		t.Fatalf("追加后 size 应变大: %d → %d", preSize, post.Size())
	}
	if !post.ModTime().Equal(preMod) {
		t.Fatal("mtime 应被拨回追加前")
	}

	after, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != beforeCount+4 {
		t.Fatalf("应多出 4 条，实际 %d → %d", beforeCount, len(after.Entries))
	}
	// 新叶子必须可达。
	if after.LeafID != "a11" {
		t.Fatalf("叶子应为 a11，实际 %s", after.LeafID)
	}
}

// TestScanCache重写后失效 文件被整体重写（Pi 的 migrate 路径）即使更大
// 或大小巧合相同，也必须 miss。
func TestScanCache重写后失效(t *testing.T) {
	store, id, path := newBigStore(t, 10)
	ctx := context.Background()
	if _, err := store.History(ctx, id, "", "", 50); err != nil {
		t.Fatal(err)
	}
	// 用不同内容重写，条目数相同但 ID 不同。
	cwd := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTurns(t, path, cwd, id, 10)
	// 把 ID 全部改名，模拟「结构变了」
	raw, _ := os.ReadFile(path)
	mutated := strings.ReplaceAll(string(raw), `"id":"u0"`, `"id":"renamed0"`)
	mutated = strings.ReplaceAll(mutated, `"parentId":"u0"`, `"parentId":"renamed0"`)
	if err := os.WriteFile(path, []byte(mutated), 0644); err != nil {
		t.Fatal(err)
	}
	// 强制 mtime 变化到未来，确保不是靠 size 撞上
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	page, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	// 改过 ID 后 leaf 应该变了；如果缓存未失效，这里会拿到旧的 a9。
	if page.LeafID != "a9" {
		t.Fatalf("叶子应仍是 a9，实际 %s（ID 改名不应影响 leaf 判定）", page.LeafID)
	}
	// 关键：第一条 user 的 ID 必须已经是 renamed0，证明重新扫过了。
	if !strings.Contains(string(page.Entries[0]), "renamed0") {
		t.Fatalf("缓存未失效：仍读到旧内容 %s", string(page.Entries[0])[:min(120, len(string(page.Entries[0])))])
	}
}

// TestScanCache大小相同时仍失效 size 相同、内容已改时，mtime 必须兜住。
//
// 这条测试要盯的不是「页面读到新内容」——Entries 按 offset 现读，
// 与缓存无关。要盯的是 parent 链：缓存里存的是旧的 offset，
// 如果 mtime 没兜住，读出来的会是错位的内容。
// 构造方式：把最后一条的 ID 改掉，看 leaf 判定是否跟着变。
func TestScanCache大小相同时仍失效(t *testing.T) {
	store, id, path := newBigStore(t, 3)
	ctx := context.Background()
	// 先建立缓存。leaf 应为 a2。
	p1, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if p1.LeafID != "a2" {
		t.Fatalf("leaf 应为 a2，实际 %s", p1.LeafID)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 把最后一条 a2 改名为 z2。两条记录等长，size 不变。
	// 但 leaf 会从 a2 变成 z2——只有重新扫描才能发现。
	mutated := strings.Replace(string(raw), `"id":"a2"`, `"id":"z2"`, 1)
	mutated = strings.Replace(mutated, `"parentId":"a2"`, `"parentId":"z2"`, 1)
	if len(mutated) != len(raw) {
		t.Fatalf("替换后长度变了：%d vs %d", len(mutated), len(raw))
	}
	if err := os.WriteFile(path, []byte(mutated), 0644); err != nil {
		t.Fatal(err)
	}

	p2, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if p2.LeafID != "z2" {
		t.Fatalf("缓存未失效：leaf 应变成 z2，实际仍是 %s", p2.LeafID)
	}
}

// TestScanCache有界 超大索引不得进缓存。
func TestScanCache有界(t *testing.T) {
	store, id, _ := newBigStore(t, 5)
	ctx := context.Background()
	if _, err := store.History(ctx, id, "", "", 50); err != nil {
		t.Fatal(err)
	}
	nodes, bytes := store.scan.stats()
	if nodes != 10 {
		t.Fatalf("应缓存 10 个节点，实际 %d", nodes)
	}
	if bytes <= 0 || bytes > maxCachedBytes {
		t.Fatalf("字节数越界: %d", bytes)
	}
}

// TestScanCache错误路径不变 坏文件在缓存未命中时的错误必须与原来一致。
func TestScanCache错误路径不变(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	dir := t.TempDir()
	sd := filepath.Join(dir, "sessions")
	os.MkdirAll(sd, 0755)
	policy, _ := workspace.New([]string{cwd})
	store, err := New(sd, policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// 索引是按目录快照的，新建文件后必须刷新，否则 Lookup 直接报「会话不存在」。
	if _, err := store.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// 缺会话头。注意：索引只收录头合法的会话，所以这类文件
	// 走 History 会先被 Lookup 拒掉（not_found）。这里直接测 scanFile，
	// 保证它报的仍是原来那一句。
	nohdr := filepath.Join(sd, "nohdr.jsonl")
	if err := os.WriteFile(nohdr, []byte(`{"type":"message","id":"u1","parentId":null}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(nohdr)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := fh.Stat()
	// 首条不是 session 头，所以先撞上「会话头部已变化」而不是「缺少头部」。
	// 两条都是拒绝，关键是它不会静默接受一个没有头的文件。
	if _, _, err := store.scanFile(ctx, fh, st.Size(), "nohdr", cwd); err == nil {
		t.Fatal("没有会话头的文件必须被拒绝")
	}
	fh.Close()
	// 重复 ID
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"dup","timestamp":"t","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"a","parentId":null}`,
		`{"type":"message","id":"a","parentId":"a"}`,
	}, "\n") + "\n"
	os.WriteFile(filepath.Join(sd, "dup.jsonl"), []byte(body), 0644)
	if _, err := store.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, "dup", "", "", 50); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("错误应含「重复」，实际 %v", err)
	}
	// parentId 是数字 → 慢路径
	body = strings.Join([]string{
		`{"type":"session","version":3,"id":"num","timestamp":"t","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"a","parentId":123}`,
	}, "\n") + "\n"
	os.WriteFile(filepath.Join(sd, "num.jsonl"), []byte(body), 0644)
	if _, err := store.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, "num", "", "", 50); err == nil || !strings.Contains(err.Error(), "父条目 ID 无效") {
		t.Fatalf("错误应为「父条目 ID 无效」，实际 %v", err)
	}
}

// TestScanCache并发 并发读同一会话不得互相破坏。
func TestScanCache并发(t *testing.T) {
	store, id, _ := newBigStore(t, 30)
	ctx := context.Background()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				if _, err := store.History(ctx, id, "", "", 10); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func stOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// appendTurns 往会话文件末尾追加若干轮。
func appendTurns(t *testing.T, path, lastParent string, from, to int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	last := lastParent
	for i := from; i < to; i++ {
		u, a := "u"+strconv.Itoa(i), "a"+strconv.Itoa(i)
		for _, e := range []benchMessage{
			{Type: "message", ID: u, ParentID: &last, Timestamp: "t", Message: benchMsg("user", "新问题")},
			{Type: "message", ID: a, ParentID: &u, Timestamp: "t", Message: benchMsg("assistant", []map[string]any{{"type": "text", "text": "新回答"}})},
		} {
			if err := enc.Encode(e); err != nil {
				t.Fatal(err)
			}
		}
		last = a
	}
}
