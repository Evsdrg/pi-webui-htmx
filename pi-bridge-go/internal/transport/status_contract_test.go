package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 两类端点的状态码契约必须分开守住。
//
// 片段端点（htmx 交换）出任何「状态类」问题都回 200 + 可读 HTML：
// htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容上且没有解释。
// 外壳与静态资源则相反——浏览器与缓存层按真实状态码判断，一旦被改成
// 200，前端 `if (!response.ok)` 会静默失效，而缓存会把错误响应也存下来。
//
// 这条测试不检查实现方式，只检查对外行为，因此两种约定各自钉住一份。
func Test片段端点状态类失败仍是200(t *testing.T) {
	requireUI(t)
	s, _, cwd := newTestServer(t)
	// 会话不存在：这是状态类失败（不是渲染失败），必须当内容渲染。
	writeSessionFile(t, s.store.Dir(), "known", cwd)
	fragments := []struct {
		name string
		path string
	}{
		{"会话列表越界分页", "/ui/sessions?offset=-1"},
		{"历史会话不存在", "/ui/sessions/absent-one/history"},
		{"模型清单", "/ui/models"},
		{"资源包清单", "/ui/packages"},
		{"文件列表越界路径", "/ui/files?path=/etc"},
		{"Git 状态越界路径", "/ui/git-status?path=/etc"},
		{"分支树未启动 worker", "/ui/branch?sessionId=absent-one"},
		{"系统提示词未启动 worker", "/ui/system?sessionId=absent-one"},
		{"工具清单未启动 worker", "/ui/tools?sessionId=absent-one"},
		{"会话详情未启动 worker", "/ui/stats?sessionId=absent-one"},
		{"差异越界路径", "/ui/diff?path=/etc"},
		{"目录选择器越界路径", "/ui/dirs?path=/etc"},
		{"记忆面板未安装", "/ui/mc?kind=memories"},
		{"搜索空词", "/ui/search"},
	}
	for _, f := range fragments {
		t.Run(f.name, func(t *testing.T) {
			rec := getUI(t, s, f.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("片段端点必须回 200（htmx 不交换 4xx/5xx），得到 %d：%s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("片段端点应回 HTML，得到 %q", ct)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("片段端点即使失败也要有可读内容")
			}
		})
	}
}

// 非片段端点保留真实状态码；把它们改成 200 就是静默破坏调用方判断。
func Test非片段端点保留真实状态码(t *testing.T) {
	requireUI(t)
	s, _, _ := newTestServer(t)
	cases := []struct {
		name string
		path string
		want int
	}{
		{"不存在的资源", "/assets/nope.js", http.StatusNotFound},
		{"越界路径的文件全文", "/ui/file-text?path=/etc/passwd", http.StatusBadRequest},
		{"越界路径的图片", "/ui/file-image?path=/etc/passwd", http.StatusBadRequest},
		{"会话 ID 非法", "/?session=../etc", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := getUI(t, s, c.path).Code; got != c.want {
				t.Fatalf("%s 应回 %d，得到 %d", c.path, c.want, got)
			}
		})
	}
}

// 扩展对话的两个端点有意偏离片段约定：204 是给前端的状态信号
// （对话已被回答 → 移除占位），非法 ID 回 400（本仓前端不可能发出）。
func Test扩展端点保留状态信号(t *testing.T) {
	requireUI(t)
	s, _, _ := newTestServer(t)
	if got := getUI(t, s, "/ui/extensions/dialog/does-not-exist").Code; got != http.StatusNoContent {
		t.Fatalf("对话不存在应回 204，得到 %d", got)
	}
	if got := getUI(t, s, "/ui/extensions/dialog/bad%2Fid").Code; got != http.StatusBadRequest {
		t.Fatalf("非法对话 ID 应回 400，得到 %d", got)
	}
}

// 外壳能正常渲染，且压缩协商仍然生效（编码改成具名类型后最容易坏的就是这里）。
func Test外壳与压缩协商(t *testing.T) {
	requireUI(t)
	s, _, _ := newTestServer(t)
	plain := getUI(t, s, "/")
	if plain.Code != http.StatusOK || !strings.Contains(plain.Body.String(), "<html") {
		t.Fatalf("外壳应正常渲染：%d", plain.Code)
	}
	if enc := plain.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("未声明 Accept-Encoding 时不应压缩，得到 %q", enc)
	}
	if v := plain.Header().Get("Vary"); v != "Accept-Encoding" {
		t.Fatalf("Vary 必须无条件声明，得到 %q", v)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("外壳渲染失败：%d", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "br" {
		t.Fatalf("声明 br 时应回 Content-Encoding: br，得到 %q", enc)
	}
}

// 静态资源的缓存语义必须留住：资源名含内容哈希，因此可长期不可变缓存。
func Test静态资源缓存头(t *testing.T) {
	requireUI(t)
	s, _, _ := newTestServer(t)
	// 从外壳里取一个真实资源名，避免猜测命名规则。
	shell := getUI(t, s, "/").Body.String()
	// 外壳里的资源路径是相对的（配合 <base>）：设备前缀部署下
	// 绝对路径会绕过前缀（B54）。
	idx := strings.Index(shell, "assets/")
	if idx < 0 {
		t.Fatal("外壳里没有静态资源引用")
	}
	if strings.Contains(shell, `"/assets/`) {
		t.Fatal("外壳不应再用根绝对路径引用静态资源")
	}
	rest := shell[idx+len("assets/"):]
	name := rest[:strings.IndexAny(rest, `"'`)]
	rec := getUI(t, s, "/assets/"+name)
	if rec.Code != http.StatusOK {
		t.Fatalf("静态资源应可获取：%d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("静态资源应可长期缓存，得到 %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("静态资源应带 Content-Type")
	}
}

// getUI 发一个带鉴权的 UI 请求。
func getUI(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}
