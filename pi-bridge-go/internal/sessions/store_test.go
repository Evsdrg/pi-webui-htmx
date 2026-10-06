package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// entryRole 造一条指定角色的消息记录，用于测试混合回合。
func entryRole(id, parent, role string) string {
	return `{"type":"message","id":"` + id + `","parentId":` + jsonQuote(parent) + `,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"` + role + `","content":"` + id + `"}}`
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

func TestHistory模型按所选分支且翻页不回退(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	id := writeSession(t, dir, "models", cwd,
		`{"type":"model_change","id":"m1","parentId":null,"provider":"old","modelId":"old-model"}`,
		entryRole("u1", "m1", "user"),
		`{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","provider":"old","model":"old-model","content":[{"type":"text","text":"旧回复"}]}}`,
		`{"type":"model_change","id":"m2","parentId":"a1","provider":"new","modelId":"new-model"}`,
		entryRole("u2", "m2", "user"),
		`{"type":"message","id":"a2","parentId":"u2","message":{"role":"assistant","provider":"new","model":"new-model","content":[{"type":"text","text":"新回复"}]}}`,
		`{"type":"model_change","id":"m3","parentId":"a1","provider":"branch","modelId":"branch-model"}`,
		entryRole("u3", "m3", "user"),
	)
	for _, tc := range []struct {
		leaf, before, provider, model string
	}{
		{"a2", "", "new", "new-model"},
		{"a2", "u2", "new", "new-model"},
		{"", "", "branch", "branch-model"},
		{"a1", "", "old", "old-model"},
	} {
		page, err := store.History(context.Background(), id, tc.leaf, tc.before, 2)
		if err != nil {
			t.Fatal(err)
		}
		if page.HistoricalModel == nil || page.HistoricalModel.Provider != tc.provider || page.HistoricalModel.ID != tc.model {
			t.Fatalf("leaf=%q before=%q: 历史模型错误: %+v", tc.leaf, tc.before, page.HistoricalModel)
		}
	}
}

// PageBytes 是一页的体积预算，不是单条记录的硬上限。叶子记录本身
// 超过页面预算时（含截图的工具结果常见 2–7 MiB），会话仍必须能打开。
func TestHistory单条超页预算仍可打开(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	huge := strings.Repeat("x", 3<<20) // 3 MiB，大于 PageBytes(2MiB)、小于 LineBytes(8MiB)
	id := writeSession(t, dir, "bigentry", cwd,
		entry("u1", ""),
		`{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":"`+huge+`"}}`,
	)
	ctx := context.Background()
	page, err := store.History(ctx, id, "", "", 10)
	if err != nil {
		t.Fatalf("叶子超过页预算不应让会话不可读: %v", err)
	}
	// 这一页至少含那条大记录（一页至少一条），并且仍能往回取到更旧的 u1。
	if len(page.Entries) == 0 || !bytes.Contains(page.Entries[len(page.Entries)-1], []byte(`"a1"`)) {
		t.Fatalf("大记录页应含叶子 a1: %d 条", len(page.Entries))
	}
	page, err = store.History(ctx, id, "", page.OldestEntryID, 10)
	if err != nil {
		t.Fatalf("大记录之后更旧的历史仍不可达: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("更旧的历史应可达")
	}
}

// 中间某条超过页面预算时，以它为页首的那一页仍应返回，否则更旧的历史
// 被永久挡住（翻页报错后就再也翻不过去）。
func TestHistory大条目不挡住更旧历史(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	huge := strings.Repeat("y", 3<<20)
	id := writeSession(t, dir, "midbig", cwd,
		entry("u1", ""),
		entry("u2", "u1"),
		`{"type":"message","id":"a2","parentId":"u2","message":{"role":"assistant","content":"`+huge+`"}}`,
		entry("u3", "a2"),
	)
	ctx := context.Background()
	// 第一页只够放最新的 u3；再往前翻，页首会落在那条大记录上。
	page, err := store.History(ctx, id, "", "", 1)
	if err != nil {
		t.Fatalf("首页失败: %v", err)
	}
	page, err = store.History(ctx, id, "", page.OldestEntryID, 1)
	if err != nil {
		t.Fatalf("翻到大条目处不应报错: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("大条目页应至少返回一条")
	}
	// 继续往前应能取到更旧的历史。
	page, err = store.History(ctx, id, "", page.OldestEntryID, 1)
	if err != nil {
		t.Fatalf("大条目之后更旧的历史仍不可达: %v", err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("更旧的历史应可达")
	}
}

func TestHistory无切换记录时从助手消息提取模型(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	id := writeSession(t, dir, "legacy", cwd,
		entryRole("u1", "", "user"),
		`{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","provider":"fallback","model":"from-message","content":[{"type":"text","text":"回复"}]}}`,
	)
	page, err := store.History(context.Background(), id, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.HistoricalModel == nil || page.HistoricalModel.Provider != "fallback" || page.HistoricalModel.ID != "from-message" {
		t.Fatalf("缺少助手消息里的历史模型: %+v", page.HistoricalModel)
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
	list, err := store.List(context.Background(), 0, 50, "")
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

func TestHistory分页对齐到完整轮(t *testing.T) {
	// 直接按原始条目数切片会把一轮切成两半，翻页回来的第一屏边界上
	// 会出现没有 user 锚点的孤儿工具/助手条目，与上一屏末尾重复。
	// 这里验证：每页的最旧一条必须是 user 消息（或已到会话开头）。
	cwd := t.TempDir()
	dir := t.TempDir()
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	lines := []string{}
	parent := ""
	for turn := 0; turn < 10; turn++ {
		u := fmt.Sprintf("u%d", turn)
		lines = append(lines, entryRole(u, parent, "user"))
		parent = u
		for k := 0; k < 2; k++ {
			tk := fmt.Sprintf("t%d_%d", turn, k)
			lines = append(lines, entryRole(tk, parent, "toolResult"))
			parent = tk
		}
		a := fmt.Sprintf("a%d", turn)
		lines = append(lines, entryRole(a, parent, "assistant"))
		parent = a
	}
	id := writeSession(t, dir, "align", cwd, lines...)
	ctx := context.Background()

	// limit=6 会把第二轮切成两半（6 条 = 1.5 轮）。
	page, err := store.History(ctx, id, "", "", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("空页")
	}
	if role := oldestRole(t, page); role != "user" {
		t.Fatalf("最旧一条应为 user，实际 %q（半轮）", role)
	}

	next, err := store.History(ctx, id, "", page.OldestEntryID, 6)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entryIDs(page.Entries) {
		seen[e] = true
	}
	for _, e := range entryIDs(next.Entries) {
		if seen[e] {
			t.Fatalf("翻页出现重复条目 %q", e)
		}
	}
	if len(next.Entries) > 0 {
		if role := oldestRole(t, next); role != "user" {
			t.Fatalf("翻页后最旧一条应为 user，实际 %q", role)
		}
	}
}

func TestHistory轮对齐不跨会话开头无限扩大(t *testing.T) {
	// 一路取到会话开头都没遇到 user 时，保留原切片而不是无限扩大。
	cwd := t.TempDir()
	dir := t.TempDir()
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := writeSession(t, dir, "nouser", cwd,
		entryRole("t1", "", "toolResult"), entryRole("t2", "t1", "toolResult"),
		entryRole("t3", "t2", "toolResult"), entryRole("t4", "t3", "toolResult"))
	page, err := store.History(context.Background(), id, "", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) == 0 || len(page.Entries) > 4 {
		t.Fatalf("条目数异常: %d", len(page.Entries))
	}
}

// oldestRole 取一页最旧一条的角色。
// 注意 Page.Entries 按祖先到后代（旧→新）排序，首位才是最旧。
func oldestRole(t *testing.T, page Page) string {
	t.Helper()
	raw := page.Entries[0]
	var v struct {
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal(string(raw))
	}
	return v.Message.Role
}

// 页条目的生命周期必须独立于「下一次读取」。
//
// 现在整页只用一次分配（各条目是同一块缓冲的子切片，见 History），
// 这是有意的：条目要活到渲染完。代价是有人可能顺手把这块缓冲也池化，
// 那会让「读完下一页后上一页内容被覆写」——本仓历史上已经出过一次
// 同类别名缺陷（FileList / DataTransfer）。这条测试把边界钉住：
// 拿到的条目在自己被丢弃之前，内容不因后续读取而改变。
func Test页条目不被后续读取改变(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	lines := []string{}
	parent := ""
	for turn := 0; turn < 12; turn++ {
		u := fmt.Sprintf("u%d", turn)
		lines = append(lines, entryRole(u, parent, "user"))
		parent = u
		a := fmt.Sprintf("a%d", turn)
		lines = append(lines, entryRole(a, parent, "assistant"))
		parent = a
	}
	id := writeSession(t, dir, "life", cwd, lines...)
	ctx := context.Background()

	first, err := store.History(ctx, id, "", "", 6)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make([]string, len(first.Entries))
	for i, e := range first.Entries {
		snapshot[i] = string(e)
	}
	// 继续翻页、换叶子读、重复读首页——这些都可能踩到共享缓冲。
	if _, err := store.History(ctx, id, "", first.OldestEntryID, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, id, "", "", 50); err != nil {
		t.Fatal(err)
	}
	for i, e := range first.Entries {
		if string(e) != snapshot[i] {
			t.Fatalf("第 %d 条在后续读取后被改变：\n前 %s\n后 %s", i, snapshot[i], e)
		}
	}
	// 内容本身也必须仍是合法 JSON 且能解出 id。
	for i, e := range first.Entries {
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(e, &v); err != nil || v.ID == "" {
			t.Fatalf("第 %d 条不再是合法条目：%v %s", i, err, e)
		}
	}
}
