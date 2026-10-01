package terminal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 终端不得继承服务凭据，但业务环境必须保留。
//
// 这里用真实 PTY 跑 /bin/sh：这是唯一能证明「子进程实际拿到的环境」
// 的办法，代价是它对外部负载敏感——失败时请看报的是超时还是内容不符。
func Test终端只继承业务环境(t *testing.T) {
	t.Setenv("PI_BRIDGE_TOKEN", "fixture-secret")
	t.Setenv("PI_RELAY_KEY", "fixture-secret")
	t.Setenv("TEST_PI_API_KEY", "business-key")
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
	if err := term.Write([]byte("printf 'RESULT:%s:%s:%s\\n' \"${PI_BRIDGE_TOKEN+x}\" \"${PI_RELAY_KEY+x}\" \"${TEST_PI_API_KEY+x}\"; exit\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out strings.Builder
	for {
		chunk, err := sub.Next(ctx)
		if err != nil {
			// 区分超时与读失败：以前两者共用一句「终端未移除服务凭据」，
			// 于是一次因负载导致的 5s 超时会报成安全问题。
			// 预算给到 15s：本测试要真启动 PTY 与 /bin/sh，在
			// `-race ./...` 全包并行时调度会被拖慢；断言本身不变。
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("等待终端输出超时：已读到 %q", out.String())
			}
			t.Fatalf("读取终端输出失败：%v（已读到 %q）", err, out.String())
		}
		out.Write(chunk)
		if strings.Contains(out.String(), "RESULT:::x") {
			return
		}
	}
}
