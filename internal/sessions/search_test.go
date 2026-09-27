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

func TestSearch结果携带会话标题与工作目录(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeSearchSession(t, dir, cwd, "s1",
		`{"type":"message","id":"u1","parentId":null,"timestamp":"t","message":{"role":"user","content":"Review the sample project"}}`+"\n"+
			`{"type":"message","id":"a1","parentId":"u1","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"Project overview"}]}}`+"\n"+
			`{"type":"session_info","id":"title","parentId":"a1","timestamp":"t","name":"Sample workspace review"}`+"\n")

	out, err := store.Search(context.Background(), "Project overview", DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0].Title != "Sample workspace review" || out.Matches[0].Cwd != cwd || out.Matches[0].EntryID != "a1" {
		t.Fatalf("搜索结果缺少标题、目录或定位条目: %+v", out.Matches)
	}
	limits := DefaultSearchLimits()
	limits.MaxMatches = 1
	out, err = store.Search(context.Background(), "Review the sample", limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 1 || out.Matches[0].Title != "Review the sample project" || out.Matches[0].Cwd != cwd {
		t.Fatalf("提前命中上限时应退回首条用户标题: %+v", out.Matches)
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

// Test搜索超大行标记截断 覆盖 B28：
// 遇到超过 LineBytes 的行时旧实现直接跳过该文件且不置 Truncated，
// 后续命中被漏掉，却向调用方报告「结果完整」。
func Test搜索超大行标记截断(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	// 第一条是普通命中；第二条大到超过 LineBytes；第三条又是命中。
	// 如果超大行不被标记，第三条会被跳过而结果仍显示完整。
	small := `{"type":"message","id":"e1","parentId":null,"timestamp":"t","message":{"role":"user","content":"命中词 甲"}}`
	huge := `{"type":"message","id":"e2","parentId":"e1","timestamp":"t","message":{"role":"assistant","content":"命中词 ` + strings.Repeat("填充", 40000) + `"}}`
	last := `{"type":"message","id":"e3","parentId":"e2","timestamp":"t","message":{"role":"user","content":"命中词 丙"}}`
	writeSearchSession(t, sessionDir, cwd, "s-big", small+"\n"+huge+"\n"+last+"\n")

	limits := DefaultSearchLimits()
	limits.LineBytes = 64 << 10 // 64 KiB，远小于 huge 那一行
	out, err := store.Search(context.Background(), "命中词", limits)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatal("跳过超大行必须标记截断，否则调用方会以为结果完整")
	}
	// 至少要被标记；命中数以不重复为基线即可，具体取决于行的实际大小。
	seen := map[string]bool{}
	for _, m := range out.Matches {
		if seen[m.EntryID] {
			t.Fatalf("出现重复命中: %s", m.EntryID)
		}
		seen[m.EntryID] = true
	}
	if len(out.Matches) == 0 {
		t.Fatal("超大行之外的命中不应被一并丢掉")
	}
}
