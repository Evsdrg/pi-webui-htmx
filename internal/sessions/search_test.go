package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// 同一文件里多条命中时，每条的摘要必须来自它自己那条记录。
//
// 分帧读取现在复用一块内部缓冲（jsonl.Reusable）。如果复用写错——比如
// 忘记从 buf[:0] 开始，或者把缓冲交给了别人——表现就是「后面的记录覆写
// 前面的内容」：条数仍然对，但摘要会重复或串到相邻记录上。这里用内容
// 互不相同的多行把这条钉住。
func TestSearch多条命中各自摘要不串行(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	var body strings.Builder
	parent := "null"
	for i := 0; i < 30; i++ {
		id := "u" + strconv.Itoa(i)
		// 每条都含搜索词，但尾部标记各不相同且长度递增，
		// 这样任何「复用缓冲残留」都会让摘要长度或尾部对不上。
		fmt.Fprintf(&body, `{"type":"message","id":"%s","parentId":%s,"timestamp":"t","message":{"role":"user","content":"命中标记 MARK-%d-%s"}}`+"\n",
			id, parent, i, strings.Repeat("x", 20+i))
		parent = `"` + id + `"`
	}
	writeSearchSession(t, dir, cwd, "s1", body.String())

	out, err := store.Search(context.Background(), "MARK-", DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Matches) != 30 {
		t.Fatalf("应有 30 条命中，得到 %d", len(out.Matches))
	}
	seen := map[string]bool{}
	for i, m := range out.Matches {
		wantID := "u" + strconv.Itoa(i)
		if m.EntryID != wantID {
			t.Fatalf("第 %d 条命中应来自 %s，得到 %s", i, wantID, m.EntryID)
		}
		wantTail := fmt.Sprintf("MARK-%d-%s", i, strings.Repeat("x", 20+i))
		if !strings.Contains(m.Snippet, wantTail) {
			t.Fatalf("第 %d 条摘要串行：期望含 %q，得到 %q", i, wantTail, m.Snippet)
		}
		if seen[m.Snippet] {
			t.Fatalf("第 %d 条摘要与前面的重复：%q", i, m.Snippet)
		}
		seen[m.Snippet] = true
	}

	// 同一查询连跑两次也必须完全一致：复用缓冲不跨调用留状态。
	again, err := store.Search(context.Background(), "MARK-", DefaultSearchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Matches) != len(out.Matches) {
		t.Fatalf("重复搜索条数不一致: %d vs %d", len(again.Matches), len(out.Matches))
	}
	for i := range out.Matches {
		if again.Matches[i] != out.Matches[i] {
			t.Fatalf("第 %d 条在重复搜索后不同:\n%+v\n%+v", i, out.Matches[i], again.Matches[i])
		}
	}
}

// B43：跳过的超大文件也要占访问预算。
// 老实现里它们既不扫描也不计数，于是一个装满大文件的目录会被逐个 stat 到底。
func Test搜索跳过超大文件也计入访问预算(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	dir := filepath.Join(sessionDir, replaceAll("--"+filepath.ToSlash(cwd)+"--", "/", "-"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// 稀疏文件：只占 stat 尺寸，不实际写盘。
	for i := 0; i < 20; i++ {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("2026-01-01T00-00-%02d.000Z_big%d.jsonl", i, i)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(8 << 20); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	limits := DefaultSearchLimits()
	limits.MaxFiles = 5
	limits.MaxFileBytes = 1 << 20
	out, err := store.Search(context.Background(), "任意", limits)
	if err != nil {
		t.Fatal(err)
	}
	if out.Scanned != 5 {
		t.Fatalf("超大文件也应计入访问预算：scanned=%d，期望 5", out.Scanned)
	}
	if !out.Truncated {
		t.Fatal("应标记 truncated")
	}
}

// B43：单文件搜索期间取消必须生效。
// 老实现只在文件之间检查取消，一个文件就能把取消拖到扫完。
func Test搜索在单文件内响应取消(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	// 前部不含搜索词：必须扫到文件末尾才有命中，
	// 这样扫描时间远大于取消延迟，也不会因命中上限提前收尾。
	plain := `{"type":"message","id":"x","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"` + strings.Repeat("无关文本", 200) + `"}}`
	hit := `{"type":"message","id":"y","parentId":"x","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"末尾命中填充"}}`
	var body strings.Builder
	for body.Len() < 24<<20 {
		body.WriteString(plain)
		body.WriteString("\n")
	}
	body.WriteString(hit)
	body.WriteString("\n")
	writeSearchSession(t, sessionDir, cwd, "big", body.String())

	limits := DefaultSearchLimits()
	limits.MaxFileBytes = 32 << 20
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, err := store.Search(ctx, "填充", limits)
	if err == nil {
		t.Fatal("取消后搜索应返回错误，而不是静默给出完整结果")
	}
}
