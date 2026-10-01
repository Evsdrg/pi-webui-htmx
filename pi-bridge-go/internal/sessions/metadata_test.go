package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test列表从持久记录读取最新会话名(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeSession(t, dir, "named", cwd, entry("u1", ""), `{"type":"session_info","id":"n1","name":"设计评审"}`)
	rows, _, _, err := store.index.Page(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].name != "设计评审" {
		t.Fatalf("会话名未读取: %+v", rows)
	}
	f, err := os.OpenFile(filepath.Join(dir, "named.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"session_info","id":"n2","name":"更新后的名称"}` + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, _, err = store.index.Page(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].name != "更新后的名称" {
		t.Fatalf("重命名后缓存未失效: %s", rows[0].name)
	}
}

func Test无名称时采用首条用户文本且忽略末尾半行(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeSession(t, dir, "unnamed", cwd, `{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"请检查  前端\n布局"}]}}`)
	f, err := os.OpenFile(filepath.Join(dir, "unnamed.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"session_info","name":"尚未写完"}`)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := store.index.Page(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].name != "请检查 前端 布局" {
		t.Fatalf("错误的标题: %q", rows[0].name)
	}
	if !rows[0].titleRead {
		t.Fatal("未缓存已读取的元数据")
	}
}

func Test标题限制Unicode字符数(t *testing.T) {
	got := shortTitle("  "+strings.Repeat("界", 1000), 80)
	if len([]rune(got)) != 81 || !strings.HasSuffix(got, "…") {
		t.Fatalf("标题未按字符截断: %q", got)
	}
}

// List/Find 必须把 name 透传给调用方：侧栏标题来自这里，
// 丢掉它会让界面只能显示会话 ID（已实际发生过一次）。
func Test列表与查找透传会话名(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeSession(t, dir, "named", cwd, entry("u1", ""), `{"type":"session_info","id":"n1","name":"透传测试"}`)
	writeSession(t, dir, "plain", cwd, `{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"无名称时取首条消息"}}`)
	listing, err := store.List(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, item := range listing.Items {
		names[item.ID] = item.Name
	}
	if names["named"] != "透传测试" {
		t.Fatalf("List 丢失会话名: %q", names["named"])
	}
	if names["plain"] != "无名称时取首条消息" {
		t.Fatalf("List 未回退到首条消息: %q", names["plain"])
	}
	header, err := store.Find(context.Background(), "named")
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "透传测试" {
		t.Fatalf("Find 丢失会话名: %q", header.Name)
	}
}
