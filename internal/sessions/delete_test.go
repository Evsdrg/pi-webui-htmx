package sessions

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDelete删除会话并刷新索引(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "del-1", time.Now())
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	out, err := store.Delete(ctx, "del-1")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if out.SessionID != "del-1" {
		t.Fatalf("删除结果异常: %+v", out)
	}
	if _, err := os.Stat(out.Path); !os.IsNotExist(err) {
		t.Fatal("文件应已删除")
	}
	if _, err := store.Find(ctx, "del-1"); err == nil {
		t.Fatal("删除后不应仍能查到")
	}
}

func TestDelete拒绝不存在的会话(t *testing.T) {
	cwd := t.TempDir()
	store, _ := newStore(t, cwd)
	if _, err := store.Delete(context.Background(), "nope"); err == nil {
		t.Fatal("不存在的会话必须被拒绝")
	}
	if _, err := store.Delete(context.Background(), "../etc/passwd"); err == nil {
		t.Fatal("非法 ID 必须被拒绝")
	}
}

func TestDelete拒绝越界与非法ID(t *testing.T) {
	cwd := t.TempDir()
	store, _ := newStore(t, cwd)
	for _, bad := range []string{"", "..", "/etc/passwd", "a/b", "../escape"} {
		if _, err := store.Delete(context.Background(), bad); err == nil {
			t.Fatalf("越界 ID %q 必须被拒绝", bad)
		}
	}
}
