package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// B63：relay 入口的 Host/Origin 校验不能因为「没配 host」而整体跳过。
//
// 老实现是 `if s.host != "" && r.Host != s.host`：host 为空时**每一条**
// 校验都被绕过，任意 Host 都能访问——DNS rebinding 与反代滥用都从这里进来。
// 与桥同一原则（架构 S09）：来源必须显式声明，没声明就不放行。
func Test未声明host时构造失败(t *testing.T) {
	registry, err := NewRegistry(t.TempDir(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registry.Close)
	users, err := NewUsers(testRelaySecret, "")
	if err != nil {
		t.Fatal(err)
	}
	// 空 host 与只有空白的 host 都必须在启动期失败，
	// 而不是运行期用 403 掩盖误配置。
	for _, bad := range []string{"", "   "} {
		if _, err := NewServer(registry, users, Config{Host: bad}); err == nil {
			t.Fatalf("未声明 host 的 relay 应构造失败: %q", bad)
		}
	}
	// 带协议或路径的值不是 Host 头，必须拒绝而不是猜。
	for _, bad := range []string{"https://relay.example", "relay.example/path"} {
		if _, err := NewServer(registry, users, Config{Host: bad}); err == nil {
			t.Fatalf("非法 host 应被拒绝: %q", bad)
		}
	}
}

// 声明了 host 之后：命名不匹配一律拒绝；Origin 若存在必须同源。
func Test声明host后严格校验Host与Origin(t *testing.T) {
	s, _, _ := newRelayServer(t)
	// 命中：本机地址（声明不含端口，端口任意）。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:30143"
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("匹配的 Host 应通过: %d", rec.Code)
	}
	// 域名不同的 Host：拒绝（大小写不敏感地比较，但不同名字就是不同名字）。
	for _, bad := range []string{"evil.example", "localhost", "127.0.0.1.evil.example"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Host = bad
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("Host=%q 应被拒绝，实际 %d", bad, rec.Code)
		}
	}
	// Origin 存在但不同源：拒绝。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:30143"
	req.Header.Set("Origin", "https://evil.example")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨源 Origin 应被拒绝，实际 %d", rec.Code)
	}
	// 同源 Origin 放行。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:30143"
	req.Header.Set("Origin", "http://127.0.0.1:30143")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("同源 Origin 应通过: %d", rec.Code)
	}
}

// 声明带端口时按精确匹配：这是「多实例共用一台机器」的用法。
func Test声明带端口时精确匹配(t *testing.T) {
	cfg := Config{Host: "relay.example:30143"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"relay.example:30143": true,
		"relay.example:9999":  false, // 端口不同即不同来源
		"relay.example":       false,
		"evil.example:30143":  false,
	}
	for host, want := range cases {
		if got := hostMatches(cfg.Host, host); got != want {
			t.Fatalf("hostMatches(%q, %q) = %v，期望 %v", cfg.Host, host, got, want)
		}
	}
}
