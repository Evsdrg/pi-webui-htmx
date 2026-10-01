package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// B75：shell 必须来自本机固定受信路径表。
// 老实现只校验 basename 并走 exec.LookPath，PATH 上任何一个叫 bash 的
// 可执行文件都会被启动。
func Test伪装成bash的可执行文件被拒绝(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "bash")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho pwned\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// 1) 绝对路径指向伪装文件：名字在白名单里，但不是受信文件。
	if _, err := resolveShell(fake); err == nil {
		t.Fatal("非受信路径的 bash 应被拒绝")
	}
	// 2) PATH 上的伪装文件不再被采用。
	t.Setenv("PATH", dir)
	resolved, err := resolveShell("bash")
	if err != nil {
		t.Fatalf("系统 bash 应仍可用: %v", err)
	}
	if resolved == fake {
		t.Fatal("不得从 PATH 取 shell（B75）")
	}
	if resolved != "/bin/bash" && resolved != "/usr/bin/bash" {
		t.Fatalf("应解析到受信路径: %q", resolved)
	}
	// 3) 受信路径本身仍可直接给出（含 /bin → /usr/bin 这类 symlink）。
	if got, err := resolveShell(resolved); err != nil || got != resolved {
		t.Fatalf("受信路径应可用: %q %v", got, err)
	}
	// 4) 名字不在表里的一律拒绝。
	if _, err := resolveShell("perl"); err == nil {
		t.Fatal("表外 shell 应被拒绝")
	}
}

// 相对路径（含分隔符但不是绝对路径）依赖进程 cwd 解析，等于让调用方
// 间接挑二进制："."、"./sh"、"bin/sh"、"../bin/sh" 全部拒绝。
// 只有裸名字（查受信表）与绝对路径（必须是表内候选的同一文件）可用。
func Test相对路径shell一律拒绝(t *testing.T) {
	for _, bad := range []string{"./sh", "bin/sh", "../bin/sh", "./bash", "sub/sh"} {
		if _, err := resolveShell(bad); err == nil {
			t.Fatalf("相对路径 %q 应被拒绝", bad)
		}
	}
	// 裸名字仍可用（走受信表）。
	if _, err := resolveShell("sh"); err != nil {
		t.Fatalf("裸名字 sh 应可用: %v", err)
	}
}

// 会话成员清点是 killSession 的基础：交互 shell 的后台作业在
// **自己的进程组**里，但 sid 仍是 shell 的 pid。按进程组杀必然漏掉它。
func Test会话成员含同会话后台作业(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := term.Subscribe(64, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := term.Write([]byte("sleep 300 & echo CHILD=$!\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := 0
	out := strings.Builder{}
	for child == 0 {
		chunk, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("未读到后代 PID: %q (%v)", out.String(), err)
		}
		out.Write(chunk)
		if mm := childPIDPattern.FindStringSubmatch(out.String()); mm != nil {
			child, _ = strconv.Atoi(mm[1])
		}
	}
	shell := term.Info().PID
	members := sessionMembers(shell)
	found := map[int]bool{}
	for _, pid := range members {
		found[pid] = true
	}
	if !found[shell] || !found[child] {
		t.Fatalf("会话成员应含 shell(%d) 与后台作业(%d): %v", shell, child, members)
	}
	// 关键差异：作业不在 shell 的进程组里，所以「按会话」这一步是必需的。
	if got := sessionOf(child); got != shell {
		t.Fatalf("作业应与 shell 同会话: sid=%d shell=%d", got, shell)
	}
}
