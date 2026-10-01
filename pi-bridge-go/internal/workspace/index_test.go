package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newIndexTest(t *testing.T) (*Files, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md")
	write("src/main.go")
	write("src/util/helper.go")
	write("docs/guide.md")
	write("node_modules/pkg/index.js") // 必须被跳过
	write(".hidden/secret.txt")
	return newFiles(t, root), root
}

func TestIndex列出文件并跳过忽略目录(t *testing.T) {
	f, root := newIndexTest(t)
	got, err := f.Index(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Files, "\n")
	for _, want := range []string{"README.md", "src/main.go", "src/util/helper.go", "docs/guide.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少 %s，实际 %v", want, got.Files)
		}
	}
	if strings.Contains(joined, "node_modules") {
		t.Errorf("node_modules 应被跳过: %v", got.Files)
	}
}

func TestIndex按basename与子序列排序(t *testing.T) {
	f, root := newIndexTest(t)
	got, err := f.Index(context.Background(), root, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) == 0 {
		t.Fatal("helper 应命中")
	}
	if got.Matches[0].Path != "src/util/helper.go" {
		t.Fatalf("basename 命中应排第一: %v", got.Matches)
	}
	// 子序列：h-l-r 能命中 helper.go
	sub, err := f.Index(context.Background(), root, "hlp")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Matches) == 0 {
		t.Fatal("子序列应命中 helper.go")
	}
}

func TestIndex带目录前缀只在对应目录找(t *testing.T) {
	f, root := newIndexTest(t)
	got, err := f.Index(context.Background(), root, "src/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || got.Matches[0].Path != "src/main.go" {
		t.Fatalf("应只命中 src/main.go: %v", got.Matches)
	}
}

func TestIndex拒绝越界与非目录(t *testing.T) {
	f, root := newIndexTest(t)
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Index(context.Background(), outside, ""); err == nil {
		t.Fatal("根外路径必须被拒绝")
	}
	if _, err := f.Index(context.Background(), filepath.Join(root, "README.md"), ""); err == nil {
		t.Fatal("普通文件必须被拒绝")
	}
}

func TestIndex缓存有界且按TTL过期(t *testing.T) {
	f, root := newIndexTest(t)
	ctx := context.Background()
	if _, err := f.Index(ctx, root, ""); err != nil {
		t.Fatal(err)
	}
	f.indexMu.Lock()
	n := len(f.indexCache)
	f.indexMu.Unlock()
	if n != 1 {
		t.Fatalf("应缓存 1 项，实际 %d", n)
	}
	// 直接把缓存标记为过期，下一次必须重建而不是继续用旧数据。
	f.indexMu.Lock()
	for _, e := range f.indexCache {
		e.expireAt = time.Now().Add(-time.Second)
	}
	f.indexMu.Unlock()
	if _, err := f.Index(ctx, root, ""); err != nil {
		t.Fatal(err)
	}
	f.indexMu.Lock()
	defer f.indexMu.Unlock()
	if len(f.indexCache) != 1 {
		t.Fatalf("重建后仍应只有 1 项，实际 %d", len(f.indexCache))
	}
}
