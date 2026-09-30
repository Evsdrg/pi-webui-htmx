package terminal

import (
	"os"
	"path/filepath"
	"testing"
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
