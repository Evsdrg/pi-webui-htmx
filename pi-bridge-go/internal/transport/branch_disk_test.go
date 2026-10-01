package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// U04：没有 worker 时分支面板也从磁盘投影出树。
// 老实现直接 manager.Get，于是打开分支面板等于要求先启动一个工作进程。
func Test无worker时分支面板从磁盘投影(t *testing.T) {
	requireUI(t)
	s, m, cwd := newTestServer(t)
	writeSessionFile(t, s.store.Dir(), "sess-1", cwd)
	if len(m.List()) != 0 {
		t.Fatalf("前置：不应有工作进程: %+v", m.List())
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/branch?sessionId=sess-1", nil)
	req.Host = s.host
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("片段应 200: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// 磁盘投影应给出可导航的节点（条目 ID 出现在 data-branch-goto 上）。
	if !strings.Contains(body, "data-branch-goto") {
		t.Fatalf("应渲染出分支节点: %s", body)
	}
	// 关键两点：浏览不启动进程，也不再是「请先显式启动会话」。
	if len(m.List()) != 0 {
		t.Fatalf("浏览分支不得启动工作进程: %+v", m.List())
	}
	if strings.Contains(body, "请先显式启动会话") {
		t.Fatalf("不应再要求先启动会话: %s", body)
	}
	// 有 worker 时仍走 Pi 的实时树；这里的磁盘路径不为 fork 提供列表。
	if !strings.Contains(body, "没有可分支的用户消息") {
		t.Fatalf("磁盘路径不应伪造 fork 列表: %s", body)
	}
}
