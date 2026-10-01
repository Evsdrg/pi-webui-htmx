package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDelete删除会话并刷新索引(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "del-1", time.Now())
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 10, ""); err != nil {
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

// TestTrash失败不降级为永久删除 覆盖 B62：
// trash 存在却执行失败时，旧实现静默退回 os.Remove，
// 把本可恢复的删除变成永久丢失。
func TestTrash失败不降级为永久删除(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "sess-trash", time.Now())
	if _, err := store.List(context.Background(), 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	// 用一个必定失败的假 trash 占据 PATH 前面的位置。
	bin := t.TempDir()
	fake := filepath.Join(bin, "trash")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := store.Delete(context.Background(), "sess-trash"); err == nil {
		t.Fatal("trash 失败时应报错，而不是静默永久删除")
	}
	// 文件必须还在：用户还有恢复机会。
	out, err := store.Find(context.Background(), "sess-trash")
	if err != nil {
		t.Fatalf("会话仍应可查询: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, filepath.FromSlash(out.path))); err != nil {
		t.Fatalf("trash 失败后会话文件被删除了: %v", err)
	}
}

// Test没有trash时才真正删除 覆盖反向边界：
// 系统确实没有 trash 是明确的无回收站环境，此时才允许 os.Remove。
func Test没有trash时才真正删除(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "sess-notrash", time.Now())
	if _, err := store.List(context.Background(), 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	// 指向一个空的 bin 目录，确保找不到 trash。
	t.Setenv("PATH", t.TempDir())

	result, err := store.Delete(context.Background(), "sess-notrash")
	if err != nil {
		t.Fatal(err)
	}
	if result.Trashed {
		t.Fatal("没有 trash 时不应标记为已回收")
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("文件应已被删除: %v", err)
	}
}
