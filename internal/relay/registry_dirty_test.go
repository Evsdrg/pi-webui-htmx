package relay

import (
	"os"
	"strings"
	"testing"
)

// Test注册表写盘失败后不清脏 覆盖 B78：
// persist 以前在序列化之后、写盘之前就清 dirty，于是写盘失败后
// 下次 persist 直接返回 nil，那次改动（含它之前所有未落盘的改动）
// 永远不会写进文件——重启即丢失。
func Test注册表写盘失败后不清脏(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRegistry(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)

	// 让写盘失败：devices.json 先占成一个目录，rename 必然失败。
	if err := os.Mkdir(r.path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("dev-a", "甲"); err != nil {
		t.Fatal(err)
	}
	if err := r.persist(); err == nil {
		t.Fatal("目标被占成目录时写盘应当失败")
	}

	// 故障解除后**不再**产生新改动，只让写盘再跑一次：失败的那次改动必须自己补上。
	// 旧实现已经在失败前清掉 dirty，所以这里会直接返回 nil，
	// 改动从此只存在于内存里（后台定时器再来也一样）。
	if err := os.Remove(r.path()); err != nil {
		t.Fatal(err)
	}
	if err := r.persist(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(r.path())
	if err != nil {
		t.Fatalf("写盘失败期间的改动没有被补写：%v", err)
	}
	if !strings.Contains(string(saved), "dev-a") {
		t.Fatalf("设备未落盘: %s", saved)
	}
}
