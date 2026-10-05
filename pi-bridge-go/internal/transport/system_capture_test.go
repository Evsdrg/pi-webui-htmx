package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	run "pi-bridge-go/internal/runtime"
)

// 本文件覆盖「面板显示实际下发的提示词」这条链路。
//
// 背景：Pi 的 export_html 快照里 systemPrompt 是扩展改写之前的基线（每轮结束 Pi
// 会复位）。真实下发给模型的那份只能由桥内捕获扩展在 before_provider_request
// 时落盘。面板必须优先显示捕获结果，并在回退到基线时如实标注来源。

// startWorkerWithCapture 起一个 worker，并往捕获结果目录写一条该会话的记录。
func startWorkerWithCapture(t *testing.T, m *run.Manager, cwd, sessionID, prompt string) {
	t.Helper()
	t.Setenv("FAKE_PI_SESSION_ID", sessionID)
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	dir := w.CaptureResultDir()
	if dir == "" {
		t.Fatal("夹具未启用捕获结果目录")
	}
	body, _ := json.Marshal(map[string]any{
		"sessionId":    sessionID,
		"systemPrompt": prompt,
		"tools": []any{
			map[string]any{"name": "read", "description": "读取文件（运行时捕获）", "parameters": map[string]any{"type": "object"}},
		},
	})
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

// 有捕获时，/ui/system 显示捕获的系统提示词，并标成「运行时捕获」而不是基线。
func Test系统面板优先显示运行时捕获(t *testing.T) {
	requireUI(t)
	s, m, cwd := newTestServer(t)
	startWorkerWithCapture(t, m, cwd, "cap-sess", "你运行在一个 coding agent harness 中（捕获版）")
	rec := getUI(t, s, "/ui/system?sessionId=cap-sess")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "你运行在一个 coding agent harness 中（捕获版）") {
		t.Fatalf("应显示捕获的系统提示词: %s", body)
	}
	if !strings.Contains(body, "运行时捕获") {
		t.Fatalf("应标注来源为运行时捕获: %s", body)
	}
}

// 有捕获时，/ui/tools 显示捕获的工具定义（含描述中的运行时标记）。
func Test工具面板优先显示运行时捕获(t *testing.T) {
	requireUI(t)
	s, m, cwd := newTestServer(t)
	startWorkerWithCapture(t, m, cwd, "cap-tools", "基线提示词")
	rec := getUI(t, s, "/ui/tools?sessionId=cap-tools")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "读取文件（运行时捕获）") {
		t.Fatalf("应显示捕获的工具描述: %s", body)
	}
	if !strings.Contains(body, "运行时捕获") {
		t.Fatalf("应标注来源为运行时捕获: %s", body)
	}
}
