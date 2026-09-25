package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSearchSession(t *testing.T, sessionDir, cwd, id, body string) {
	t.Helper()
	dir := filepath.Join(sessionDir, replaceAll("--"+filepath.ToSlash(cwd)+"--", "/", "-"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, "2026-01-01T00-00-00.000Z_"+id+".jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSearch命中与摘要(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeSearchSession(t, sessionDir, cwd, "s1",
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"请修复登录 Bug"}}`+"\n"+
			`{"type":"message","id":"b","parentId":"a","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"已定位到 token 过期"}]}}`+"\n")
	writeSearchSession(t, sessionDir, cwd, "s2",
		`{"type":"message","id":"c","parentId":null,"timestamp":"2026-01-01T00:00:03.000Z","message":{"role":"user","content":"无关内容"}}`+"\n")

	out, err := store.Search(context.Background(), "登录", DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0].SessionID != "s1" || out.Matches[0].EntryID != "a" {
		t.Fatalf("搜索结果异常: %+v", out.Matches)
	}
	if out.Matches[0].Role != "user" || !strings.Contains(out.Matches[0].Snippet, "登录") {
		t.Fatalf("摘要异常: %+v", out.Matches[0])
	}
	if out.Scanned != 2 {
		t.Fatalf("扫描文件数异常: %d", out.Scanned)
	}
	if out.Truncated {
		t.Fatal("不应标记截断")
	}
}

func TestSearch大小写不敏感(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeSearchSession(t, sessionDir, cwd, "s1",
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"Hello World"}}`+"\n")
	for _, q := range []string{"hello", "HELLO", "WoRlD"} {
		out, err := store.Search(context.Background(), q, DefaultSearchLimits())
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Matches) != 1 {
			t.Fatalf("查询 %q 应命中: %+v", q, out.Matches)
		}
	}
}

func TestSearch上限与截断(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(`{"type":"message","id":"e` + string(rune('a'+i%26)) + `","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"命中词` + string(rune('a'+i%26)) + `"}}` + "\n")
	}
	writeSearchSession(t, sessionDir, cwd, "s1", b.String())
	limits := DefaultSearchLimits()
	limits.MaxMatches = 5
	out, err := store.Search(context.Background(), "命中词", limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 5 || !out.Truncated {
		t.Fatalf("上限未生效: %d %v", len(out.Matches), out.Truncated)
	}
}

func TestSearch参数校验(t *testing.T) {
	cwd := t.TempDir()
	store, _ := newStore(t, cwd)
	for _, bad := range []string{"", "   ", strings.Repeat("x", 201)} {
		if _, err := store.Search(context.Background(), bad, DefaultSearchLimits()); err == nil {
			t.Fatalf("查询 %q 应被拒绝", bad)
		}
	}
}

func TestSearch忽略末尾半行(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session","version":3,"id":"half","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"完整命中"}}` + "\n" +
		`{"type":"message","id":"b","parentId":"a","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"半行`
	if err := os.WriteFile(filepath.Join(sessionDir, "half.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := New(sessionDir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	out, err := store.Search(context.Background(), "命中", DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0].EntryID != "a" {
		t.Fatalf("末尾半行应被忽略: %+v", out.Matches)
	}
}

func TestSearch不修改文件(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeSearchSession(t, sessionDir, cwd, "s1",
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"内容"}}`+"\n")
	path := filepath.Join(sessionDir, replaceAll("--"+filepath.ToSlash(cwd)+"--", "/", "-"), "2026-01-01T00-00-00.000Z_s1.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Search(context.Background(), "内容", DefaultSearchLimits()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("搜索不得修改会话文件")
	}
}
