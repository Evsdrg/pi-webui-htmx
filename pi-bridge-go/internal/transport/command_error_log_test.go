package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/pi"
	"pi-bridge-go/internal/protocol"
)

// captureLog 把 slog 默认输出换成缓冲区，返回「取走当前内容」的函数。
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string { return buf.String() }
}

// 已送达 Pi 但执行失败：必须记下原因，否则「stop 为什么失败」「set_model
// 报了什么」只剩一个 outcome 类别，错误文本无处可查。
func Test命令失败记录原因(t *testing.T) {
	s, _, _ := newTestServer(t)
	taken := captureLog(t)
	s.logCommandFailure("session.set_model", "sess-1", &pi.RPCError{Message: "模型未找到：Model not found"})
	got := taken()
	if !strings.Contains(got, "命令失败") || !strings.Contains(got, "session.set_model") {
		t.Fatalf("应记下方法与失败事实: %q", got)
	}
	if !strings.Contains(got, "模型未找到：Model not found") {
		t.Fatalf("应记下 Pi 返回的错误文本: %q", got)
	}
}

// 「未送达」的一类（busy / limit_exceeded …）是正常控制流：重同步与忙拒绝
// 会被频繁触发。全记会把日志淹掉，反而看不到真正的故障，因此这类不记。
// 这里直接遍历 notExecutedCodes 的事实来源，新增错误码不会悄悄漏出覆盖。
func Test未送达Pi的拒绝不记日志(t *testing.T) {
	s, _, _ := newTestServer(t)
	for code := range notExecutedCodes {
		taken := captureLog(t)
		s.logCommandFailure("session.subscribe", "sess-1", protocol.E(code, "拒绝"))
		if got := taken(); got != "" {
			t.Fatalf("控制流拒绝 %q 不应记进日志，实际: %q", code, got)
		}
	}
	// 反向对照：不在该表里的失败必须记——否则上面整个用例可能是因为
	// logCommandFailure 什么都不记而恒真。
	taken := captureLog(t)
	s.logCommandFailure("session.subscribe", "sess-1", protocol.E("worker_exited", "工作进程已退出"))
	if got := taken(); !strings.Contains(got, "worker_exited") {
		t.Fatalf("非控制流的失败应记日志: %q", got)
	}
}

// 结论未知（桥没能判定命令是否执行）同样值得记：它是崩溃恢复的信号。
func Test结论未知也记日志(t *testing.T) {
	s, _, _ := newTestServer(t)
	taken := captureLog(t)
	s.logCommandFailure("session.prompt", "sess-1", protocol.E("outcome_unknown", "命令结果未知"))
	if got := taken(); !strings.Contains(got, "outcome_unknown") {
		t.Fatalf("未知结论应记日志: %q", got)
	}
}

// 只记方法与错误，不记 params：params 可能含用户正文或凭据。
// logCommandFailure 的签名根本不接收 params，这里从反面钉住这一点：
// 调用方就算把整条请求传进来，日志里也只能有方法、错误码与错误文本。
func Test命令失败日志不含请求参数(t *testing.T) {
	s, _, _ := newTestServer(t)
	taken := captureLog(t)
	s.logCommandFailure("session.prompt", "sess-1", fmt.Errorf("上游返回 401: %s", "Invalid API key"))
	got := taken()
	// 正常错误仍要保留，否则这个用例会恒真。
	if !strings.Contains(got, "Invalid API key") {
		t.Fatalf("应保留错误文本: %q", got)
	}
	if strings.Contains(got, "{") || strings.Contains(got, "params") {
		t.Fatalf("不应记录参数结构: %q", got)
	}
}

// errors.Is/As 能穿透包装：真实路径上错误常被 %w 包过一层。
func Test命令失败识别被包装的错误(t *testing.T) {
	s, _, _ := newTestServer(t)
	taken := captureLog(t)
	wrapped := fmt.Errorf("派发失败: %w", &pi.RPCError{Message: "Connection error."})
	s.logCommandFailure("session.stop", "sess-1", wrapped)
	if got := taken(); !strings.Contains(got, "Connection error.") {
		t.Fatalf("被包装的错误也应记下: %q", got)
	}
	// busy 被包装后仍属「未送达」，不应记。
	taken = captureLog(t)
	s.logCommandFailure("session.subscribe", "sess-1", fmt.Errorf("包装: %w", protocol.E("busy", "忙")))
	if got := taken(); got != "" {
		t.Fatalf("被包装的 busy 也不应记: %q", got)
	}
}

// 端到端：真实 spawn 出的假 Pi 对 set_model 回失败，桥必须把 Pi 的错误文本
// 记进日志。这是最初的目的——「set_model 到底报了什么」此前只剩一个
// outcome 类别，错误文本无处可查。
func Test失败命令原因端到端落进桥日志(t *testing.T) {
	t.Setenv("FAKE_PI_SET_MODEL_ERROR", "模型未找到：Model not found（端到端）")
	s, _, cwd := newTestServer(t)
	taken := captureLog(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	defer conn.CloseNow()
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("响应不是 JSON: %s", b)
		}
		return m
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "e2e-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	if started["ok"] != true {
		t.Fatalf("session.start 应成功: %v", started)
	}
	data, _ := started["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "e2e-2", "sessionId": sessionID, "method": "session.set_model", "params": map[string]any{"provider": "acc-live", "modelId": "acc-live"}})
	if r := read(); r["ok"] == true {
		t.Fatalf("set_model 应当失败: %v", r)
	}
	got := taken()
	if !strings.Contains(got, "命令失败") || !strings.Contains(got, "session.set_model") {
		t.Fatalf("桥日志应记下失败的 set_model: %q", got)
	}
	if !strings.Contains(got, "模型未找到：Model not found（端到端）") {
		t.Fatalf("桥日志应含 Pi 返回的错误文本: %q", got)
	}
}
