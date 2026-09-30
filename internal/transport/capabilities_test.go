package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pi-bridge-go/internal/terminal"
)

// Test能力发现只报实际生效的值 覆盖 B80：
// 发现端点以前写死 `phase: "A"`（阶段早已完成）与终端的默认限额
// （4 / 600 秒）——而 CLI 的 --max-terminals / --terminal-idle 能改掉它们，
// 于是「报给客户端的限额」和「真正执行的限额」是两回事，调用方按报告
// 规划并发就会撞上真实的 limit_exceeded。
func Test能力发现只报实际生效的值(t *testing.T) {
	s, _, _ := newTestServerTuned(t, 2*time.Second, func(c *terminal.Config) {
		c.MaxTerminals = 7
		c.IdleTimeout = 90 * time.Second
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	// 桥会核对 Host 与凭据，测试请求必须带上它们。
	req.Host = s.host
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("能力端点应返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var caps map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatalf("能力响应不是 JSON: %v", err)
	}
	if _, ok := caps["phase"]; ok {
		t.Fatalf("阶段字段已经过时（阶段早完成），不该再报: %v", caps["phase"])
	}
	limits, _ := caps["limits"].(map[string]any)
	if got := limits["terminals"]; got != float64(7) {
		t.Fatalf("terminals 应报实际限额 7，实际 %v", got)
	}
	if got := limits["terminalIdleSeconds"]; got != float64(90) {
		t.Fatalf("terminalIdleSeconds 应报实际值 90，实际 %v", got)
	}
}
