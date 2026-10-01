package transport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// timeAt 给第 i 个夹具文件一个递增的修改时间（过去一小时内的第 i 分钟），
// 让「裁掉最旧的」可以按名字断言。
func timeAt(i int) time.Time {
	return time.Now().Add(-time.Hour).Add(time.Duration(i) * time.Minute)
}

func readNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// newTestTunnel 建一条把回帧收进内存的隧道，供不需要真实浏览器的用例使用。
func newTestTunnel(t *testing.T, s *Server, mu *sync.Mutex, sent *[][]byte) *TunnelBridge {
	t.Helper()
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		mu.Lock()
		defer mu.Unlock()
		*sent = append(*sent, frame)
		return nil
	}, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)
	return bridge
}

// Test导出目录按数量裁掉最旧的 覆盖 B77 的数量维度。
func Test导出目录按数量裁掉最旧的(t *testing.T) {
	dir := t.TempDir()
	// mtime 递增，便于断言「留下的是最新的」。
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("e-%02d.html", i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := timeAt(i)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneExports(dir, 4, 1<<20); err != nil {
		t.Fatal(err)
	}
	left := readNames(t, dir)
	if len(left) != 4 {
		t.Fatalf("应裁到 4 个，实际 %d: %v", len(left), left)
	}
	for _, name := range left {
		if name < "e-08.html" {
			t.Fatalf("留下了过旧的产物 %s（应保留最新的 4 个）", name)
		}
	}
}

// Test导出目录按字节裁掉最旧的 覆盖 B77 的体积维度：
// 单个产物可能很大（大会话的 HTML 内嵌全部条目），只限数量不够。
func Test导出目录按字节裁掉最旧的(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		path := filepath.Join(dir, fmt.Sprintf("big-%02d.html", i))
		if err := os.WriteFile(path, make([]byte, 1000), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := timeAt(i)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneExports(dir, 100, 2500); err != nil {
		t.Fatal(err)
	}
	left := readNames(t, dir)
	if len(left) > 2 {
		t.Fatalf("2500 字节上限下最多留 2 个，实际 %d: %v", len(left), left)
	}
	for _, name := range left {
		if name < "big-04.html" {
			t.Fatalf("留下了过旧的产物 %s", name)
		}
	}
}

// Test反复导出不会撑爆导出目录 是 B77 的端到端版本：
// 先放一批历史产物，再真的走一次 session.export_html，
// 目录必须被收在上限内，且刚导出的那个文件不能被自己删掉。
func Test反复导出不会撑爆导出目录(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	// 导出是磁盘投影：有会话文件即可，不需要先启动工作进程（B76）。
	writeSessionFile(t, s.store.Dir(), "sess-1", cwd)
	for i := 0; i < maxExportFiles+5; i++ {
		path := filepath.Join(s.exportDir, fmt.Sprintf("old-%03d.html", i))
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := timeAt(i)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var sent [][]byte
	bridge := newTestTunnel(t, s, &mu, &sent)
	export := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "exp1", "sessionId": "sess-1",
		"method": "session.export_html", "params": map[string]any{"fileName": "session-check.html"},
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", export))
	waitFrames(t, &mu, &sent, 1)
	if reply := decodeFrame(t, lastFrame(t, &mu, &sent)); reply["ok"] != true {
		t.Fatalf("导出失败: %v", reply)
	}

	left := readNames(t, s.exportDir)
	if len(left) > maxExportFiles {
		t.Fatalf("导出目录未被收在上限内：%d > %d", len(left), maxExportFiles)
	}
	kept := false
	for _, name := range left {
		if name == "session-check.html" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("刚导出的产物被裁掉了: %v", left)
	}
}
