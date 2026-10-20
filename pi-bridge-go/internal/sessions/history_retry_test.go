package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 索引与磁盘内容错位时（Pi 的 fork / 版本迁移会原地重写整个文件），
// History 应自己重扫一次，而不是把「读取期间历史文件发生变化」丢给用户。
//
// 为什么必须自愈：这条错误对用户没有任何可操作性——刷新一下就好，而
// 界面上的「加载更早」按钮点下去只看到一句话，用户只能反复点。既然
// 重扫是本进程内几十毫秒的事，就不该让用户替我们重试。
//
// 复现手法：造一份会话并读一次（索引进缓存），然后**原地**重写文件、
// 把长度挪到别的行上，再把 mtime 改回原值——缓存键（size/mtime/dev/ino）
// 全部命中，于是按旧偏移读出来的字节必然错位。
func Test索引错位时自动重扫(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	const id = "shift"
	header := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`
	// 两条记录，填充长度互换：总字节数不变，行边界整体平移。
	first := func(pad int) string {
		return fmt.Sprintf(`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"%s"}}`, strings.Repeat("p", pad))
	}
	second := func(pad int) string {
		return fmt.Sprintf(`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":"%s"}}`, strings.Repeat("q", pad))
	}
	before := strings.Join([]string{header, first(600), second(200)}, "\n") + "\n"
	after := strings.Join([]string{header, first(200), second(600)}, "\n") + "\n"
	if len(before) != len(after) {
		t.Fatalf("两份内容必须等长，实际 %d / %d", len(before), len(after))
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(before), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// 第一次读：建索引并进缓存。
	if _, err := store.History(ctx, id, "", "", 10); err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	// 原地重写 + 还原 mtime，让缓存键全部命中。
	path := filepath.Join(dir, id+".jsonl")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(after); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}

	// 第二次读：旧偏移读出的字节错位，必须靠自愈重扫纠正。
	page, err := store.History(ctx, id, "", "", 10)
	if err != nil {
		t.Fatalf("索引错位应自动重扫，不该把错误交给用户: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("重扫后应读到 2 条，实际 %d 条", len(page.Entries))
	}
	if !strings.Contains(string(page.Entries[0]), `"u1"`) {
		t.Fatalf("重扫后条目顺序/内容不对: %s", page.Entries[0])
	}
}
