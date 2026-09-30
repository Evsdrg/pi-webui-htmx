package transport

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pngOf 造一个能被魔数识别、指定载荷体积的 PNG 假图。
func pngOf(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
}

// Test图片命令超帧预算时明确报错 覆盖 B33：
// workspace 允许 4 MiB 的图片，而 WS 单帧只有 512 KiB。旧实现把 base64
// 直接塞进响应，超限帧被连接层静默丢掉——命令看起来卡住，用户只等到超时，
// 而「大部分被识别为受支持的图片」都在这个区间里。
func Test图片命令超帧预算时明确报错(t *testing.T) {
	s, _, cwd := newTestServer(t)
	// base64 会放大到 4/3，取刚好越过预算的体积。
	bigSize := wsTextBudget*3/4 + 1024
	if err := os.WriteFile(filepath.Join(cwd, "big.png"), pngOf(bigSize), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "small.png"), pngOf(2048), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)
	boot := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "boot",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", boot))
	waitFrames(t, &mu, &sent, 1)
	started := decodeFrame(t, lastFrame(t, &mu, &sent))
	startedData, _ := started["data"].(map[string]any)
	sessionID, _ := startedData["sessionId"].(string)

	// 小图仍要能通过事件通道返回：收紧边界不能把正常尺寸的图一起挡掉。
	small := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "small", "sessionId": sessionID,
		"method": "files.image", "params": map[string]any{"path": filepath.Join(cwd, "small.png")},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", small))
	waitFrames(t, &mu, &sent, 2)
	smallReply := decodeFrame(t, lastFrame(t, &mu, &sent))
	if smallReply["ok"] != true {
		t.Fatalf("小图应当照常返回: %v", smallReply)
	}
	smallData, _ := smallReply["data"].(map[string]any)
	if encoded, _ := smallData["data"].(string); encoded == "" {
		t.Fatalf("小图没有带 base64: %v", smallReply)
	}

	// 大图必须明确报错，并且说清该走哪条通道。
	big := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "big", "sessionId": sessionID,
		"method": "files.image", "params": map[string]any{"path": filepath.Join(cwd, "big.png")},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", big))
	waitFrames(t, &mu, &sent, 3)
	bigReply := decodeFrame(t, lastFrame(t, &mu, &sent))
	if bigReply["ok"] != false {
		t.Fatalf("超预算的大图应当明确失败，而不是静默丢帧: %v", bigReply)
	}
	errObj, _ := bigReply["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "limit_exceeded" {
		t.Fatalf("错误码应为 limit_exceeded，实际 %v", errObj)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "/ui/file-image") {
		t.Fatalf("错误信息应指出可用的 HTTP 通道，实际 %q", msg)
	}
}

// Test图片HTTP端点不受帧预算限制 是上一条的另一半：
// 超预算的图片仍要能通过 /ui/file-image 拿到完整字节——否则这个
// 「改用 HTTP」的建议就是空话。
func Test图片HTTP端点不受帧预算限制(t *testing.T) {
	s, _, cwd := newTestServer(t)
	bigSize := wsTextBudget*3/4 + 1024
	path := filepath.Join(cwd, "big.png")
	if err := os.WriteFile(path, pngOf(bigSize), 0600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/file-image?path="+url.QueryEscape(path), nil)
	s.serveFileImage(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 图片端点应当返回 200，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type 应为 image/png，实际 %q", got)
	}
	if rec.Body.Len() != bigSize+8 {
		t.Fatalf("返回字节数应为 %d，实际 %d", bigSize+8, rec.Body.Len())
	}
	// base64 后远超 WS 预算，正是 WS 命令拒绝的那一张。
	if encoded := base64.StdEncoding.EncodeToString(rec.Body.Bytes()); len(encoded) <= wsTextBudget {
		t.Fatalf("夹具无效：这张图 base64 后只有 %d 字节，没有越过 WS 预算 %d", len(encoded), wsTextBudget)
	}
}
