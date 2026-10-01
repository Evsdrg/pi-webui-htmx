package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"pi-bridge-go/internal/testutil"
)

// TestBash命令载荷形状：桥发出的 bash 命令必须带 command 与 excludeFromContext，
// 顶层 id 只能是桥生成的配对 id（rpc-N）。
//
// 这条锁住一个此前看不见的事实：Pi 用命令的**顶层 id** 给 bash_execution_update
// 事件打标，而顶层 id 由 pi.Client 生成并用于响应配对——同一个字段担两个职责。
// 桥侧曾经把客户端的 requestId 塞进 fields["id"]，而 Call 会过滤掉它，
// 于是那个参数从未到达过 Pi，测试也发现不了（没有任何地方观察过真实载荷）。
// 现在把它钉住：客户端无法影响 bash 命令的 id。
func TestBash命令载荷形状(t *testing.T) {
	cmdsFile := filepath.Join(t.TempDir(), "cmds.jsonl")
	m, cwd := newTestManager(t, func(c *Config) {
		c.Env = append(c.Env, "FAKE_PI_CMDS_FILE="+cmdsFile)
	})
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Bash(context.Background(), "echo hi", true); err != nil {
		t.Fatal(err)
	}

	payload := testutil.WaitForCommand(t, cmdsFile, "bash")
	if payload["command"] != "echo hi" {
		t.Fatalf("command 未正确传递: %v", payload)
	}
	if payload["excludeFromContext"] != true {
		t.Fatalf("excludeFromContext 未正确传递: %v", payload)
	}
	id, _ := payload["id"].(string)
	if !strings.HasPrefix(id, "rpc-") {
		t.Fatalf("顶层 id 应为桥生成的配对 id（rpc-N），得到 %q", id)
	}
}
