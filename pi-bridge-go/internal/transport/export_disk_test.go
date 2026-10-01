package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"pi-bridge-go/internal/protocol"
)

// B76：导出是磁盘投影，不启动工作进程。
// 老实现先 manager.Get 再让 Pi 写文件：只看历史不发送消息的导出也要拉一个进程。
func Test导出不启动工作进程(t *testing.T) {
	requireUI(t)
	s, m, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "sess-1", cwd)
	if len(m.List()) != 0 {
		t.Fatalf("前置：不应有工作进程: %+v", m.List())
	}
	params, _ := json.Marshal(map[string]any{"fileName": "session-sess-1.html"})
	reply, err := s.dispatchSessionOps(context.Background(), protocol.Request{Version: 1, Method: "session.export_html", SessionID: "sess-1", Params: params})
	if err != nil {
		t.Fatalf("无 worker 的导出应成功: %v", err)
	}
	rep, ok := reply.(exportReply)
	if !ok || rep.Path == "" {
		t.Fatalf("回执应带产物路径: %#v", reply)
	}
	body, err := os.ReadFile(rep.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "hi") {
		t.Fatalf("导出应包含会话内容: %s", text)
	}
	// 桥的导出是自包含文档，且**不含脚本**——Pi Web 的导出正因内嵌递归树脚本，
	// 长线性会话打开时会栈溢出（B45）。
	if strings.Contains(text, "<script") {
		t.Fatalf("导出文档不得含脚本: %s", text)
	}
	if len(m.List()) != 0 {
		t.Fatalf("导出不得启动工作进程: %+v", m.List())
	}
}

// B77：整段「裁剪 → 写入」必须在同一临界区里。
// 老实现只在裁剪时持锁，随后各自解锁写文件：并发导出一起看到空位，
// 目录最终超过数量上限。
func Test并发导出仍受配额约束(t *testing.T) {
	s, _, cwd := newTestServer(t)
	requireUI(t)
	writeSessionFile(t, s.store.Dir(), "sess-1", cwd)
	var wg sync.WaitGroup
	for i := 0; i < maxExportFiles+8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params, _ := json.Marshal(map[string]any{"fileName": fmt.Sprintf("conc-%d.html", i)})
			_, _ = s.dispatchSessionOps(context.Background(), protocol.Request{Version: 1, Method: "session.export_html", SessionID: "sess-1", Params: params})
		}(i)
	}
	wg.Wait()
	entries, err := os.ReadDir(s.exportDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > maxExportFiles {
		t.Fatalf("并发导出超出数量上限: %d > %d", len(entries), maxExportFiles)
	}
	// 产物必须是完整文档（原子写入）：不存在写了一半的临时文件残留。
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("导出目录残留临时文件: %s", e.Name())
		}
	}
}
