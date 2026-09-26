package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// asT 把 testing.TB 转成 *testing.T，供只收 *testing.T 的辅助使用。
func asT(tb testing.TB) *testing.T {
	if t, ok := tb.(*testing.T); ok {
		return t
	}
	return &testing.T{}
}

// buildTree 造一个指定规模的文件树。
func buildTree(tb testing.TB, files int) string {
	tb.Helper()
	root := tb.TempDir()
	dirs := []string{"", "src", "src/util", "docs", "docs/guide"}
	for _, d := range dirs {
		if d != "" {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0755); err != nil {
				tb.Fatal(err)
			}
		}
	}
	for i := 0; i < files; i++ {
		d := dirs[i%len(dirs)]
		p := filepath.Join(root, filepath.FromSlash(d), "f"+itoa(i)+".go")
		if err := os.WriteFile(p, []byte("package x\n"), 0644); err != nil {
			tb.Fatal(err)
		}
	}
	// node_modules 必须被跳过，放几个文件进去验证它真的没被扫。
	nm := filepath.Join(root, "node_modules", "pkg")
	if err := os.MkdirAll(nm, 0755); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(nm, "m"+itoa(i)+".js"), []byte("x"), 0644); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var out []byte
	for v > 0 {
		out = append([]byte{byte('0' + v%10)}, out...)
		v /= 10
	}
	return string(out)
}

// BenchmarkIndex无查询 是 @ 菜单刚打出 @ 时的路径：拿全量列表。
func BenchmarkIndex无查询(b *testing.B) {
	for _, n := range []int{200, 2000} {
		root := buildTree(b, n)
		f := newFiles(asT(b), root)
		ctx := context.Background()
		// 预热，避开首次冷启动。
		if _, err := f.Index(ctx, root, ""); err != nil {
			b.Fatal(err)
		}
		b.Run(itoa(n)+"个文件", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := f.Index(ctx, root, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkIndex有查询 是每敲一个字时的路径：服务端排序。
func BenchmarkIndex有查询(b *testing.B) {
	root := buildTree(b, 2000)
	f := newFiles(asT(b), root)
	ctx := context.Background()
	if _, err := f.Index(ctx, root, ""); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Index(ctx, root, "f199"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIndexGit 只在仓库里跑，衡量 git ls-files 这条路。
// 没有 git 或不是仓库时跳过，不让基准因为环境差异失败。
func BenchmarkIndexGit(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("没有 git")
	}
	root := buildTree(b, 2000)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Skipf("git %v 失败: %s", args, out)
		}
	}
	f := newFiles(asT(b), root)
	ctx := context.Background()
	if _, err := f.Index(ctx, root, ""); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Index(ctx, root, ""); err != nil {
			b.Fatal(err)
		}
	}
}
