package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGit子模块不启动内联差异辅助程序(t *testing.T) {
	source := initGitRepo(t)
	root := initGitRepo(t)
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := execCommand("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("准备子模块失败：%v %s", err, out)
		}
	}
	run(root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	run(root, "commit", "-qm", "add submodule")
	sub := filepath.Join(root, "sub")
	if err := os.WriteFile(filepath.Join(sub, "README.md"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(sub, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "change submodule")
	marker := filepath.Join(t.TempDir(), "executed")
	helper := filepath.Join(t.TempDir(), "helper")
	body := "#!/bin/sh\nprintf x > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\nexit 0\n"
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	setGitConfig(t, root, "diff.submodule", "diff")
	setGitConfig(t, sub, "diff.external", helper)
	diff, _, err := newFiles(t, root).GitDiff(context.Background(), root, false, 4096)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("子模块内联差异执行了辅助程序")
	}
	if err != nil || !strings.Contains(diff, "Subproject commit") {
		t.Fatalf("应返回子模块提交摘要：%v %q", err, diff)
	}
}
