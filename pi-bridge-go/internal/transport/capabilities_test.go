package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/terminal"
)

// Test能力发现只报实际生效的值 覆盖 B80：
// 发现端点以前写死 `phase: "A"`（阶段早已完成）与终端的默认限额
// （4 / 600 秒）——而 CLI 的 --max-terminals / --terminal-idle 能改掉它们，
// 于是「报给客户端的限额」和「真正执行的限额」是两回事，调用方按报告
// 规划并发就会撞上真实的 limit_exceeded。
func Test能力发现只报实际生效的值(t *testing.T) {
	s, _, _, _, _ := newTestServerTuned(t, 2*time.Second, func(c *terminal.Config) {
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

// capabilities 报的限额必须是**真实容量**，不能是手抄的副本。
//
// 这条测试的价值在于抓「只改一侧」：把 channel 容量调大、或把响应预算
// 调小，而声明没跟着改。历史上一份手抄副本报出过
// wsRequestBytes = 1 MiB（实际 97 MiB）与 wsResponseBytes = 512 KiB
// （实际 448 KiB）——前端按声明做预检时会把合法请求拦下来。
func Test能力声明与真实容量一致(t *testing.T) {
	s, _, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Host = s.host
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)

	var caps struct {
		Limits map[string]int `json:"limits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatalf("解析 capabilities 失败: %v", err)
	}

	// 每一项都与「运行时真正在用的那个数」比，而不是与另一个字面量比。
	cases := []struct {
		key  string
		want int
		why  string
	}{
		{"connections", cap(s.connections), "连接槽容量"},
		{"inFlightOperations", cap(s.operations), "在途命令预算"},
	}
	for _, c := range cases {
		if got := caps.Limits[c.key]; got != c.want {
			t.Errorf("%s 报 %d，实际容量 %d（%s）", c.key, got, c.want, c.why)
		}
	}
	// 帧预算：这两项就是 B80 里报错的那两个。
	if got := caps.Limits["wsRequestBytes"]; got != wsReadLimit {
		t.Errorf("wsRequestBytes 报 %d，实际读上限 %d", got, wsReadLimit)
	}
	if got := caps.Limits["wsResponseBytes"]; got != wsTextBudget {
		t.Errorf("wsResponseBytes 报 %d，实际响应预算 %d", got, wsTextBudget)
	}
	if got := caps.Limits["connectionQueueBytes"]; got != outboundQueueLimit {
		t.Errorf("connectionQueueBytes 报 %d，实际出站队列预算 %d", got, outboundQueueLimit)
	}
	if got := caps.Limits["requestIdsPerConnection"]; got != s.claims.maxHold {
		t.Errorf("requestIdsPerConnection 报 %d，实际去重窗口 %d", got, s.claims.maxHold)
	}
	g := run.Defaults()
	if got := caps.Limits["replayItems"]; got != g.ReplayItems {
		t.Errorf("replayItems 报 %d，实际 %d", got, g.ReplayItems)
	}
	if got := caps.Limits["replayBytes"]; got != g.ReplayBytes {
		t.Errorf("replayBytes 报 %d，实际 %d", got, g.ReplayBytes)
	}
	wantTerminals, wantIdle := s.terminals.Limits()
	if got := caps.Limits["terminals"]; got != wantTerminals {
		t.Errorf("terminals 报 %d，实际 %d", got, wantTerminals)
	}
	if got := caps.Limits["terminalIdleSeconds"]; got != wantIdle {
		t.Errorf("terminalIdleSeconds 报 %d，实际 %d", got, wantIdle)
	}
}
