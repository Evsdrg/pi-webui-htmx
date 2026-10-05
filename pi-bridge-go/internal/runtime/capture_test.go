package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件覆盖桥内捕获扩展的读回：/ui/system 与 /ui/tools 靠它显示「实际下发给
// 模型」的系统提示词与工具定义。捕获文件由扩展以「先写 .tmp 再改名」写入，
// 桥按会话 id 读取，并对会话不匹配、空值、路径穿越做防御。

func writeCapture(t *testing.T, dir, sessionID, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCapturedRequest读回同会话捕获(t *testing.T) {
	dir := t.TempDir()
	writeCapture(t, dir, "sess-1", `{"sessionId":"sess-1","systemPrompt":"你运行在 harness 中","tools":[{"name":"read","description":"读取","parameters":{"type":"object"}}],"at":1}`)
	w := &Worker{captureResultDir: dir}
	got, ok := w.CapturedRequest("sess-1")
	if !ok {
		t.Fatal("应读回捕获结果")
	}
	if got.SystemPrompt != "你运行在 harness 中" || len(got.Tools) != 1 || got.Tools[0].Name != "read" {
		t.Fatalf("捕获内容异常: %+v", got)
	}
}

// 结果目录里的文件属于别的会话时必须拒绝：同一目录可能残留其它会话的文件。
func TestCapturedRequest会话不匹配时拒绝(t *testing.T) {
	dir := t.TempDir()
	writeCapture(t, dir, "sess-1", `{"sessionId":"sess-2","systemPrompt":"别的会话"}`)
	w := &Worker{captureResultDir: dir}
	if _, ok := w.CapturedRequest("sess-1"); ok {
		t.Fatal("会话 id 不匹配时必须拒绝")
	}
}

// 尚未产生请求（无文件）或未启用（无目录）时返回 false，调用方回退到基线。
func TestCapturedRequest无捕获时回退(t *testing.T) {
	w := &Worker{}
	if _, ok := w.CapturedRequest("sess-1"); ok {
		t.Fatal("未启用捕获时应返回 false")
	}
	w2 := &Worker{captureResultDir: t.TempDir()}
	if _, ok := w2.CapturedRequest("sess-1"); ok {
		t.Fatal("无捕获文件时应返回 false")
	}
	// 空 systemPrompt 也视为无效：面板不能把空字符串当成真值。
	writeCapture(t, w2.captureResultDir, "sess-1", `{"sessionId":"sess-1","systemPrompt":"","tools":[]}`)
	if _, ok := w2.CapturedRequest("sess-1"); ok {
		t.Fatal("空 systemPrompt 应视为无效")
	}
}

// 会话 id 带路径分隔符时不得拼出目录外路径（扩展本应只写 UUID，仍做防御）。
func TestCapturedRequest拒绝路径穿越(t *testing.T) {
	dir := t.TempDir()
	w := &Worker{captureResultDir: dir}
	for _, bad := range []string{"../etc/passwd", "a/b", "a\\b"} {
		if _, ok := w.CapturedRequest(bad); ok {
			t.Fatalf("非法会话 id %q 必须被拒绝", bad)
		}
	}
}
