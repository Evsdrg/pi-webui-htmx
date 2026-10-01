package testutil

import (
	"testing"
	"time"
)

const (
	// WaitTimeout 是条件等待的统一上限。
	//
	// 取值考虑 -race：`go test -race ./...` 下多个测试包并行运行，调度延迟被放大，
	// 过短的等待会把正常的时序抖动报成失败（历史上终端测试就因为 5 秒读超时
	// 不够而出现 flaky，见 docs/DEVELOPMENT.md）。这里统一取 5 秒。
	WaitTimeout = 5 * time.Second
	// PollInterval 是条件轮询间隔。条件本身是廉价的内存状态检查，
	// 因此可以密集轮询以缩短等待开销。
	PollInterval = 5 * time.Millisecond
)

// WaitFor 轮询 cond 直到为真；超时则让测试失败。
//
// what 描述等待的目标，必须能独立读懂（例如"补发完成后的序号"），
// 超时消息会带上它——只有"等待条件超时"无法定位是哪一步卡住。
func WaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(WaitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(PollInterval)
	}
	t.Fatalf("等待超时（%s）: %s", WaitTimeout, what)
}
