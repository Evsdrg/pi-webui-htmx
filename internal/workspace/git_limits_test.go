package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGit状态截断及响应转义预算(t *testing.T) {
	root := initGitRepo(t)
	for i := 0; i < 9000; i++ {
		name := fmt.Sprintf("%04d-%s", i, strings.Repeat("<", 240))
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := newFiles(t, root)
	f.limits.MaxEntries = 10000
	status, err := f.GitStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status["truncated"] != true || status["clean"] != false {
		t.Fatal("截断状态不明确")
	}
	body, err := json.Marshal(status)
	if err != nil || len(body) > 500<<10 {
		t.Fatalf("状态未给 WS 信封留下预算：%d %v", len(body), err)
	}
	index, err := f.Index(context.Background(), root, "0000")
	if err != nil || !index.Truncated || len(index.Matches) == 0 {
		t.Fatalf("Git 索引未传递截断状态：%+v %v", index, err)
	}
	for _, file := range status["files"].([]map[string]string) {
		if _, err := os.Stat(filepath.Join(root, file["path"])); err != nil {
			t.Fatal("状态包含半条文件名")
		}
	}
}

func TestGit恰好达到条目上限不误报截断(t *testing.T) {
	root := initGitRepo(t)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	f := newFiles(t, root)
	f.limits.MaxEntries = 5
	status, err := f.GitStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status["truncated"] != false || len(status["files"].([]map[string]string)) != 5 {
		t.Fatal("恰好达到上限被误报截断")
	}
	if err := os.WriteFile(filepath.Join(root, "sixth"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	status, err = f.GitStatus(context.Background(), root)
	if err != nil || status["truncated"] != true || len(status["files"].([]map[string]string)) != 5 {
		t.Fatalf("条目上限失效：%v", err)
	}
}

func TestGitDiff精确字节边界(t *testing.T) {
	root := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := newFiles(t, root)
	full, _, err := f.GitDiff(context.Background(), root, false, 4096)
	if err != nil || len(full) == 0 {
		t.Fatalf("无法生成差异：%v", err)
	}
	for _, size := range []int{len(full), len(full) - 1} {
		text, truncated, err := f.GitDiff(context.Background(), root, false, size)
		if err != nil || text != full[:size] || truncated != (size < len(full)) {
			t.Fatalf("字节边界错误：size=%d truncated=%v err=%v", size, truncated, err)
		}
	}
}

func TestGit重命名文件保留原名(t *testing.T) {
	root := initGitRepo(t)
	cmd := execCommand("git", "mv", "README.md", "new name.md")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("准备重命名失败：%v %s", err, out)
	}
	status, err := newFiles(t, root).GitStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	files := status["files"].([]map[string]string)
	if len(files) != 1 || files[0]["path"] != "new name.md" || files[0]["from"] != "README.md" {
		t.Fatalf("重命名分帧错误：%v", files)
	}
}
