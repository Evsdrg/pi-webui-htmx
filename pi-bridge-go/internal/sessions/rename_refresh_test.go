package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// appendSessionInfo 模拟 Pi 的 set_session_name：往会话文件尾部追加一条 session_info。
func appendSessionInfo(t *testing.T, path, name string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"session_info","id":"rename","name":"` + name + `"}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
}

func listName(t *testing.T, store *Store) string {
	t.Helper()
	rows, err := store.List(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Items) != 1 {
		t.Fatalf("应列出 1 条会话，实际 %d", len(rows.Items))
	}
	return rows.Items[0].Name
}

func nestedPath(t *testing.T, store *Store) string {
	t.Helper()
	rows, err := store.List(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Items) != 1 {
		t.Fatalf("应列出 1 条会话，实际 %d", len(rows.Items))
	}
	return filepath.Join(store.Dir(), filepath.FromSlash(rows.Items[0].path))
}

// 真实 Pi 会话几乎全部躺在 cwd 编码的子目录里（顶层只有极少数）。往子目录内的
// 文件追加一条 session_info（这正是 Pi 重命名做的事），既不改变顶层文件数、
// 也不改变任何顶层条目的 mtime——父目录 mtime 只在直接子项增删时变化，孙辈内容
// 写入不会触发它。因此索引的顶层戳判定看不见这次追加。
//
// 这条测试把「索引自己发现不了」钉成规格：不能指望目录戳，必须由外部显式通知。
// 若将来有人给索引加了「自动发现嵌套追加」的机制（例如递归 mtime），
// 这条断言会失败，提醒他同时更新 NoteExternalRename 的契约与注释。
func Test嵌套布局的追加无法被索引自身发现(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeNested(t, dir, cwd, "nested1", time.Now())

	if got := listName(t, store); got != "hi" {
		t.Fatalf("首次标题应为回退值 hi，实际 %q", got)
	}
	appendSessionInfo(t, nestedPath(t, store), "重命名后的标题")

	// 未通知索引：重列仍读缓存，标题不变——这正是「改名不生效」的表征。
	if got := listName(t, store); got != "hi" {
		t.Fatalf("未通知时不应自动发现（这正是需要 hook 的原因），实际 %q", got)
	}
}

// NoteExternalRename 是桥在收到 Pi 的 session_info_changed 时调用的入口。
// 它必须让下一次列表重建索引、并重新读回文件尾部的最新标题。
//
// 关键在于 titleForPage 一旦读过就置 titleRead、之后直接返回缓存值；
// 只清顶层戳不足以奏效，必须走完整的 Invalidate（同时清掉 builtAt 与指纹，
// 让 build 真正重走一遍、用 size 变化判定缓存条目失效）。
func TestNoteExternalRename让嵌套会话重读标题(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeNested(t, dir, cwd, "nested2", time.Now())

	if got := listName(t, store); got != "hi" {
		t.Fatalf("首次标题应为 hi，实际 %q", got)
	}
	appendSessionInfo(t, nestedPath(t, store), "重命名后的标题")
	store.NoteExternalRename()

	if got := listName(t, store); got != "重命名后的标题" {
		t.Fatalf("通知索引后应读到新标题，实际 %q", got)
	}
}

// 分组视图读的是同一个缓存标题，必须同样受益。
func TestNoteExternalRename对分组视图同样生效(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeNested(t, dir, cwd, "nested3", time.Now())

	if _, err := store.ListGrouped(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	appendSessionInfo(t, nestedPath(t, store), "分组里的新名字")
	store.NoteExternalRename()

	listing, err := store.ListGrouped(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Groups) != 1 || len(listing.Groups[0].Items) != 1 {
		t.Fatalf("分组视图应有一组一条，实际 %+v", listing.Groups)
	}
	if got := listing.Groups[0].Items[0].Name; got != "分组里的新名字" {
		t.Fatalf("分组视图应读到新名，实际 %q", got)
	}
}
