package terminal

import (
	"sync"
	"testing"
	"time"
)

func Test反复关闭终端释放注册表与配额(t *testing.T) {
	cfg := Defaults()
	cfg.MaxTerminals = 1
	m := NewManager(cfg)
	defer m.Close()
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		term, err := m.Open(dir, "/bin/sh", 80, 24)
		if err != nil {
			t.Fatalf("第 %d 次无法打开: %v", i, err)
		}
		pid := term.Info().PID
		if err := m.CloseTerminal(term.ID()); err != nil {
			t.Fatal(err)
		}
		if len(m.List()) != 0 {
			t.Fatal("已关闭的终端仍占注册表")
		}
		if processAlive(pid) {
			t.Fatal("关闭后仍有存活进程")
		}
	}
}

func Test自然退出同样移除终端记录(t *testing.T) {
	m := NewManager(Defaults())
	defer m.Close()
	term, err := m.Open(t.TempDir(), "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("终端没有退出")
	}
	if len(m.List()) != 0 {
		t.Fatal("自然退出后仍残留记录")
	}
	if _, err := m.Get(term.ID()); err == nil {
		t.Fatal("已退出终端仍可获取")
	}
}

func Test并发打开不突破终端上限(t *testing.T) {
	cfg := Defaults()
	cfg.MaxTerminals = 2
	m := NewManager(cfg)
	defer m.Close()
	dir := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	opened := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Open(dir, "/bin/sh", 80, 24); err == nil {
				mu.Lock()
				opened++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if opened != 2 || len(m.List()) != 2 {
		t.Fatalf("并发限额失效: 打开 %d 个，登记 %d 个", opened, len(m.List()))
	}
}
