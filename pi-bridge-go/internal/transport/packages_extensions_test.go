package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖「设置→扩展」面板能看到随 Pi 自动加载的扩展文件。
//
// 背景：面板原先只列 settings.json 的 packages，<agent-dir>/extensions 下自动加载
// 的 *.ts/*.js 扩展整个看不到——用户会以为自己的本地扩展没装上。接线后这里必须
// 同时列出扩展文件。

func Test扩展面板列出自动加载的扩展文件(t *testing.T) {
	requireUI(t)
	s, _, _, _, _ := newTestServerTuned(t, 2*time.Second)
	// 往被测 server 的受管 agent 目录写一个扩展文件。
	agentDir := s.piConfig.AgentDir()
	if agentDir == "" {
		t.Fatal("测试服务器未暴露 agent 目录")
	}
	extDir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "zh-system-prompt.ts"), []byte("export default () => {}"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := getUI(t, s, "/ui/packages")
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "zh-system-prompt.ts") {
		t.Fatalf("扩展面板应列出自动加载的扩展文件: %s", body)
	}
	if !strings.Contains(body, "自动加载的扩展文件") {
		t.Fatalf("应有扩展文件分组标题: %s", body)
	}
}
