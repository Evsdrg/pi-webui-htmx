package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-bridge-go/internal/workspace"
)

// writeSession 写入一个 v3 会话文件并返回其会话头 ID。
func writeSession(t *testing.T, dir, id, cwd string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := []string{`{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`}
	body = append(body, lines...)
	if err := os.WriteFile(path, []byte(strings.Join(body, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return id
}

func entry(id, parent string) string {
	return `{"type":"message","id":"` + id + `","parentId":` + jsonQuote(parent) + `,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"` + id + `"}}`
}

func jsonQuote(v string) string {
	if v == "" {
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func newStore(t *testing.T, roots ...string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	policy, err := workspace.New(roots)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(sessionDir, policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, sessionDir
}

func TestHistory按分支从叶子向上分页(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	// 主链 a->b->c，另有一条从 b 分出的分支 d。
	id := writeSession(t, sessionDir, "s1", cwd,
		entry("a", ""), entry("b", "a"), entry("c", "b"), entry("d", "b"))
	ctx := context.Background()
	page, err := store.History(ctx, id, "", "", 10)
	if err != nil {
		t.Fatalf("读取历史失败: %v", err)
	}
	if page.LeafID != "d" || page.HasMore {
		t.Fatalf("磁盘叶子与分页标记异常: %+v", page)
	}
	ids := entryIDs(page.Entries)
	if strings.Join(ids, ",") != "a,b,d" {
		t.Fatalf("未按分支取页: %v", ids)
	}
	// 指定叶子走另一条分支。
	page, err = store.History(ctx, id, "c", "", 10)
	if err != nil {
		t.Fatalf("指定叶子读取失败: %v", err)
	}
	if strings.Join(entryIDs(page.Entries), ",") != "a,b,c" {
		t.Fatalf("指定叶子分支错误: %v", entryIDs(page.Entries))
	}
	// before 必须位于所选分支，否则显式拒绝。
	if _, err := store.History(ctx, id, "c", "d", 10); err == nil {
		t.Fatal("跨分支游标应被拒绝")
	}
}

func TestHistory分页游标与体积上限(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := writeSession(t, sessionDir, "s2", cwd,
		entry("a", ""), entry("b", "a"), entry("c", "b"), entry("d", "c"))
	ctx := context.Background()
	page, err := store.History(ctx, id, "", "", 2)
	if err != nil {
		t.Fatalf("首页失败: %v", err)
	}
	if strings.Join(entryIDs(page.Entries), ",") != "c,d" || !page.HasMore || page.OldestEntryID != "c" {
		t.Fatalf("首页分页异常: %+v", page)
	}
	page, err = store.History(ctx, id, "", "c", 2)
	if err != nil {
		t.Fatalf("上一页失败: %v", err)
	}
	if strings.Join(entryIDs(page.Entries), ",") != "a,b" || page.HasMore {
		t.Fatalf("上一页分页异常: %+v", page)
	}
}

func TestHistory拒绝损坏与不完整记录(t *testing.T) {
	cwd := t.TempDir()
	cases := map[string]string{
		"重复ID":   entry("a", "") + "\n" + entry("a", "a"),
		"断链":     entry("a", "") + "\n" + entry("b", "missing"),
		"自环":     entry("a", "a"),
		"非法JSON": "{不是 JSON}",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			id := writeSession(t, dir, "bad", cwd, strings.Split(extra, "\n")...)
			store, _ := newStore(t, cwd)
			// 直接指向该临时目录，绕过 newStore 自建的 sessions 目录。
			_ = id
			_ = store
			s2, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			if _, err := s2.History(context.Background(), "bad", "", "", 10); err == nil {
				t.Fatalf("%s 应被拒绝", name)
			}
		})
	}
}

func TestHistory忽略末尾半行(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	path := filepath.Join(dir, "half.jsonl")
	content := `{"type":"session","version":3,"id":"half","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		entry("a", "") + "\n" + `{"type":"message","id":"b","parentId":"a"`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.History(context.Background(), "half", "", "", 10)
	if err != nil {
		t.Fatalf("末尾半行应被忽略，实际 %v", err)
	}
	if strings.Join(entryIDs(page.Entries), ",") != "a" {
		t.Fatalf("末尾半行处理错误: %v", entryIDs(page.Entries))
	}
}

func TestList隐藏越界工作区会话(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeSession(t, sessionDir, "mine", cwd, entry("a", ""))
	writeSession(t, sessionDir, "theirs", other, entry("a", ""))
	list, err := store.List(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "mine" {
		t.Fatalf("越界会话应被隐藏: %+v", list.Items)
	}
}

func TestValidID拒绝路径字符(t *testing.T) {
	for _, bad := range []string{"", "../etc/passwd", "a/b", "a b", strings.Repeat("x", 129)} {
		if ValidID(bad) {
			t.Fatalf("应拒绝 %q", bad)
		}
	}
	if !ValidID("abc-DEF_123") {
		t.Fatal("合法 ID 被拒绝")
	}
}

func mustPolicy(t *testing.T, roots ...string) *workspace.Policy {
	t.Helper()
	p, err := workspace.New(roots)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func entryIDs(entries []json.RawMessage) []string {
	out := []string{}
	for _, raw := range entries {
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &e) != nil {
			out = append(out, "?")
			continue
		}
		out = append(out, e.ID)
	}
	return out
}
