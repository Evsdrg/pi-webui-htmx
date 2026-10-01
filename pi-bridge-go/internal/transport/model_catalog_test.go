package transport

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// postFragment 以已认证的 POST 取片段。
func postFragment(t *testing.T, s *Server, path string, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 应返回 200，得到 %d：%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// fakeCatalog 是 models.dev 的替身：可统计请求次数，用来验证缓存。
func fakeCatalog(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	body := `{"p1":{"models":{
	  "deepseek-chat":{"name":"DeepSeek Chat","limit":{"context":128000,"output":8192}},
	  "deepseek-reasoner":{"name":"DeepSeek Reasoner","limit":{"context":128000,"output":65536},"reasoning":true,"input":["text","image"]},
	  "gpt-x":{"name":"GPT X"}
	}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// B70：目录候选由桥渲染成可点击的行，带补全所需的参数。
func Test模型目录片段渲染候选与参数(t *testing.T) {
	s := newTestServerWithUI(t)
	var hits int32
	s.piConfig.SetCatalogURL(fakeCatalog(t, &hits).URL)

	body := postFragment(t, s, "/ui/models/catalog", url.Values{})
	if !strings.Contains(body, `data-catalog-id="p1/deepseek-reasoner"`) {
		t.Fatalf("应渲染候选 ID: %s", body)
	}
	// 参数必须一起带出来，否则「补全」只能补个名字。
	for _, want := range []string{
		`data-catalog-name="DeepSeek Reasoner"`,
		`data-catalog-ctx="128000"`,
		`data-catalog-max="65536"`,
		`data-catalog-reasoning="1"`,
		`data-catalog-image="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("候选缺少 %s: %s", want, body)
		}
	}
	// 非推理、无图片的模型不该被标成 1。
	if !strings.Contains(body, `data-catalog-id="p1/gpt-x"`) || strings.Contains(body, `data-catalog-id="p1/gpt-x" data-catalog-name="GPT X" data-catalog-ctx="" data-catalog-max="" data-catalog-reasoning="1"`) {
		t.Fatalf("无能力字段的模型不应标 1: %s", body)
	}
	if !strings.Contains(body, "目录：3 条") {
		t.Fatalf("应显示目录条数: %s", body)
	}
}

// 目录按输入过滤：候选列表是服务端筛出来的，不是把整份目录塞给浏览器。
func Test模型目录片段按查询过滤(t *testing.T) {
	s := newTestServerWithUI(t)
	var hits int32
	s.piConfig.SetCatalogURL(fakeCatalog(t, &hits).URL)

	body := postFragment(t, s, "/ui/models/catalog", url.Values{"q": {"reasoner"}})
	if !strings.Contains(body, "deepseek-reasoner") {
		t.Fatalf("过滤后应保留匹配项: %s", body)
	}
	if strings.Contains(body, "gpt-x") {
		t.Fatalf("过滤后不应含未匹配项: %s", body)
	}
	if !strings.Contains(body, "匹配 1") {
		t.Fatalf("应显示匹配条数: %s", body)
	}
	// 搜索词要回显在标题上：列表已被过滤，用户得看得出是按什么筛的。
	// 只断言「结果对」不够——过滤在服务端完成，标题是唯一能看出
	// 「这已经是筛选后的视图」的地方。
	if !strings.Contains(body, "按 “reasoner” 过滤") {
		t.Fatalf("标题应回显搜索词: %s", body)
	}
	// 查不到时给出可读空态，而不是空白。
	empty := postFragment(t, s, "/ui/models/catalog", url.Values{"q": {"nothing-matches"}})
	if !strings.Contains(empty, "没有匹配的模型") {
		t.Fatalf("空结果应有说明: %s", empty)
	}
}

// 目录是公网数据：一段会话里只拉一次，后续按键走缓存。
func Test模型目录片段复用缓存(t *testing.T) {
	s := newTestServerWithUI(t)
	var hits int32
	s.piConfig.SetCatalogURL(fakeCatalog(t, &hits).URL)

	for i := 0; i < 3; i++ {
		postFragment(t, s, "/ui/models/catalog", url.Values{"q": {"deepseek"}})
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("目录应只拉取一次（缓存 10 分钟），实际 %d 次", got)
	}
}

// 目录最多 2000 条，一次渲染全部会让片段体积失控；只回前 50 条。
func Test模型目录片段限制单次候选数(t *testing.T) {
	s := newTestServerWithUI(t)
	var b strings.Builder
	b.WriteString(`{"p":{"models":{`)
	for i := 0; i < 80; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"m`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('0' + i/26)))
		b.WriteString(`":{"name":"x"}`)
	}
	b.WriteString(`}}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	s.piConfig.SetCatalogURL(srv.URL)

	body := postFragment(t, s, "/ui/models/catalog", url.Values{})
	if got := strings.Count(body, "data-catalog-id="); got != catalogMatches {
		t.Fatalf("单次候选应限制为 %d 条，实际 %d", catalogMatches, got)
	}
	if !strings.Contains(body, "目录：80 条") {
		t.Fatalf("仍应报告目录总条数: %s", body)
	}
}

// Test模型目录缓存过期后会重新拉取：缓存条件必须是「有内容 **且** 未过期」。
// 写成「或」的话，第一次拉取成功后 items 非空，缓存就永远不会失效——
// 上游目录更新（比如新模型上线）在这个进程里再也看不到。
// TTL 是 10 分钟，测试不能等，直接把时间戳拨回过去。
func Test模型目录缓存过期后会重新拉取(t *testing.T) {
	s := newTestServerWithUI(t)
	var hits int32
	s.piConfig.SetCatalogURL(fakeCatalog(t, &hits).URL)

	postFragment(t, s, "/ui/models/catalog", url.Values{"q": {"deepseek"}})
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("首次请求应拉取一次，实际 %d 次", got)
	}

	s.catalog.mu.Lock()
	s.catalog.at = time.Now().Add(-2 * catalogTTL)
	s.catalog.mu.Unlock()

	body := postFragment(t, s, "/ui/models/catalog", url.Values{"q": {"deepseek"}})
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("缓存过期后应重新拉取，实际 %d 次（缓存永不失效会让目录更新看不到）", got)
	}
	// 重新拉取的仍是真实目录，不是空壳。
	if !strings.Contains(body, "DeepSeek Chat") {
		t.Fatalf("过期后的响应应包含目录内容: %s", body)
	}
}
