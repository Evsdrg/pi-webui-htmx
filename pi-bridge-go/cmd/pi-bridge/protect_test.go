package main

import (
	"path/filepath"
	"testing"
)

// 受保护目录清单必须来自「桥自己的可执行文件」，而不是 --pi（那是 Pi 的路径）。
//
// 这个混淆在实测里造成过真实缺口：deploy/ 下同时放着桥二进制与 ui/，
// 而 ui/ 因为 --ui-dir 被保护，看上去整个部署目录已经安全——实际上
// deploy/pi-bridge 可被上传覆盖，等于远程代码执行。
func Test保护清单来自桥自身而非Pi(t *testing.T) {
	tmp := t.TempDir()
	self := filepath.Join(tmp, "deploy", "pi-bridge")
	pi := filepath.Join(tmp, "pnpm", "pi")
	ui := filepath.Join(tmp, "deploy", "ui")
	state := filepath.Join(tmp, "state")
	agent := filepath.Join(tmp, "agent")

	got := protectWritable(self, ui, state, agent)
	want := map[string]bool{
		filepath.Join(tmp, "deploy"): true,
		ui:                           true,
		state:                        true,
		agent:                        true,
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("清单里出现意外目录：%s", g)
		}
		delete(want, g)
	}
	for missing := range want {
		t.Errorf("清单缺少目录：%s", missing)
	}
	if len(got) != 4 {
		t.Errorf("清单长度 %d，期望 4：%v", len(got), got)
	}
	// Pi 的目录不应因为 --pi 而进保护清单：它是外部工具，与本服务的
	// 生命周期无关，误加进去会让用户无法上传到自己的工具链目录。
	piDir := filepath.Dir(pi)
	for _, g := range got {
		if g == piDir {
			t.Errorf("Pi 所在目录不该进保护清单：%s", g)
		}
	}
}

// 空路径要跳过，而不是把当前目录当受保护路径加进去。
func Test保护清单跳过空路径(t *testing.T) {
	got := protectWritable("", "", "", "")
	if len(got) != 0 {
		t.Fatalf("全空时应返回空清单，实际 %v", got)
	}
}
