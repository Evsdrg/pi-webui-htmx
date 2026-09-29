package terminal

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func TestOpen执行命令并回收进程(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	dir := t.TempDir()
	term, err := m.Open(dir, "/bin/sh", 80, 24)
	if err != nil {
		t.Fatalf("打开终端失败: %v", err)
	}
	pid := term.Info().PID
	if pid == 0 {
		t.Fatal("终端缺少 PID")
	}
	sub, err := term.Subscribe(64, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := term.Write([]byte("echo terminal-ok\nexit\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := strings.Builder{}
	for {
		chunk, err := sub.Next(ctx)
		if err != nil {
			break
		}
		got.Write(chunk)
		if strings.Contains(got.String(), "terminal-ok") {
			break
		}
	}
	if !strings.Contains(got.String(), "terminal-ok") {
		t.Fatalf("未收到命令输出: %q", got.String())
	}
	if err := term.ForceClose(); err != nil {
		t.Fatalf("关闭终端失败: %v", err)
	}
	if processAlive(pid) {
		t.Fatal("关闭后终端进程仍存活")
	}
}

func Test拒绝带参数的shell(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	for _, bad := range []string{"/bin/sh -c evil", "sh;rm -rf /", "../bin/sh", "/usr/bin/vim", "/bin/sh;id", "python3"} {
		if _, err := m.Open(t.TempDir(), bad, 80, 24); err == nil {
			t.Fatalf("应拒绝 %q", bad)
		}
	}
}

func Test终端数量上限(t *testing.T) {
	cfg := Defaults()
	cfg.MaxTerminals = 2
	m := NewManager(cfg)
	defer m.Close()
	dir := t.TempDir()
	opened := []*Terminal{}
	for i := 0; i < 2; i++ {
		term, err := m.Open(dir, "/bin/sh", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, term)
	}
	if _, err := m.Open(dir, "/bin/sh", 80, 24); err == nil {
		t.Fatal("超过上限应被拒绝")
	}
	if len(m.List()) != 2 {
		t.Fatalf("终端列表异常: %+v", m.List())
	}
	for _, term := range opened {
		if err := term.ForceClose(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResize与不存在终端(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	if _, err := m.Get("不存在"); err == nil {
		t.Fatal("不存在的终端应报错")
	}
	if err := m.CloseTerminal("不存在"); err == nil {
		t.Fatal("关闭不存在的终端应报错")
	}
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.ForceClose()
	if err := term.Resize(0, 0); err == nil {
		t.Fatal("非法尺寸应被拒绝")
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("调整尺寸失败: %v", err)
	}
	info := term.Info()
	if info.Cols != 120 || info.Rows != 40 {
		t.Fatalf("尺寸未生效: %+v", info)
	}
}

func Test空输入与超长输入被拒绝(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.ForceClose()
	if err := term.Write(nil); err == nil {
		t.Fatal("空输入应被拒绝")
	}
	if err := term.Write(make([]byte, 128<<10)); err == nil {
		t.Fatal("超长输入应被拒绝")
	}
}

func Test关闭后订阅与写入报错(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.ForceClose(); err != nil {
		t.Fatal(err)
	}
	if err := term.Write([]byte("x")); err == nil {
		t.Fatal("关闭后写入应失败")
	}
	if _, err := term.Subscribe(8, 1024); err == nil {
		t.Fatal("关闭后订阅应失败")
	}
}

func Test环境变量可覆盖默认shell(t *testing.T) {
	// t.Setenv 自动恢复原值，且禁止该测试并行执行。
	t.Setenv("SHELL", "/bin/sh")
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.ForceClose()
	if term.Info().Cwd == "" {
		t.Fatal("终端缺少工作目录")
	}
}
