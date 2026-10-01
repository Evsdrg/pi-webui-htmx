package testutil

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// WaitForCommand 读假 Pi 记录下来的命令（由 FAKE_PI_CMDS_FILE 指定），
// 返回第一条 type 等于 method 的命令载荷。
//
// 用法：测试里让假 Pi 把收到的命令写盘，然后断言「桥实际发出去了什么」。
// 没有这个出口时，参数是否真的到达 Pi 只能靠推断——本仓恰好有一处参数
// 从未到达过（被 RPC 层的 id 过滤吞掉），而没有任何测试能发现。
//
// 返回 map 里的数字会被解码成 float64（标准 json.Unmarshal 行为）。
func WaitForCommand(t *testing.T, path, method string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(WaitTimeout)
	for time.Now().Before(deadline) {
		if payload := findCommand(path, method); payload != nil {
			return payload
		}
		time.Sleep(PollInterval)
	}
	t.Fatalf("假 Pi 未在 %s 内记录到 %s 命令（文件 %s）", WaitTimeout, method, path)
	return nil
}

// findCommand 扫描记录文件，找第一条匹配的命令；未找到返回 nil。
// 文件由假 Pi 追加写，可能正读到半行——解析失败的行走跳过。
func findCommand(path, method string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(line), &payload) != nil {
			continue
		}
		if payload["type"] == method {
			return payload
		}
	}
	return nil
}
