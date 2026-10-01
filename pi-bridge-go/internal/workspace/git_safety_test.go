package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setGitConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	cmd := execCommand("git", "config", key, value)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("设置测试 Git 配置失败：%v %s", err, out)
	}
}

func TestGit查询不执行仓库辅助命令(t *testing.T) {
	for _, key := range []string{"core.fsmonitor", "diff.external", "diff.audit.textconv", "filter.audit.clean", "filter.audit.process"} {
		t.Run(key, func(t *testing.T) {
			root := initGitRepo(t)
			marker := filepath.Join(t.TempDir(), "executed")
			helper := filepath.Join(t.TempDir(), "helper")
			body := "#!/bin/sh\nprintf x > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\nexit 0\n"
			if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			setGitConfig(t, root, key, helper)
			if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("README.md diff=audit filter=audit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f := newFiles(t, root)
			var err error
			if key == "core.fsmonitor" || strings.HasPrefix(key, "filter.") {
				_, err = f.GitStatus(context.Background(), root)
			} else {
				_, _, err = f.GitDiff(context.Background(), root, false, 4096)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("只读查询执行了仓库辅助命令")
			}
			if strings.HasPrefix(key, "filter.") {
				if err == nil {
					t.Fatal("带转换过滤器的仓库应明确拒绝")
				}
			} else if err != nil {
				t.Fatalf("禁用非必要 helper 后普通查询应成功：%v", err)
			}
		})
	}
}

func TestGit空仓库与特殊文件名(t *testing.T) {
	root := t.TempDir()
	cmd := execCommand("git", "init", "-q", "--initial-branch=main", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init：%v %s", err, out)
	}
	name := " space\n\"quoted\".txt"
	if err := os.WriteFile(filepath.Join(root, name), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := newFiles(t, root).GitStatus(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" {
		t.Fatalf("空仓库分支错误：%v", status.Branch)
	}
	files := status.Files
	if len(files) != 1 || files[0].Path != name {
		t.Fatalf("特殊文件名被损坏：%v", files)
	}
}

func TestGit忽略继承的仓库定位(t *testing.T) {
	outside := initGitRepo(t)
	root := t.TempDir()
	f := newFiles(t, root)
	t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	if _, err := f.GitStatus(context.Background(), root); err == nil {
		t.Fatal("查询被环境变量改投到未授权仓库")
	}
}

func TestGit不越过授权子目录读取整库(t *testing.T) {
	root := initGitRepo(t)
	sub := filepath.Join(root, "allowed")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newFiles(t, sub).GitStatus(context.Background(), sub); err == nil {
		t.Fatal("授权子目录的查询越界到整库")
	}
}
