package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 对外部署的关键不变量：声明了 --public-origin 之后，桥接受的是
// 「监听地址本身」与「该来源」这两组 (Host, Origin)，其余一律拒绝；
// 未声明时行为与本地用法完全一致。
func Test公开来源的Host与Origin核对(t *testing.T) {
	s, _, _ := newTestServer(t)
	s.publicOrigin = PublicOrigin{Scheme: "https", Host: "example.com:39080"}
	s.host = "10.0.0.1:39081"

	cases := []struct {
		name   string
		host   string
		origin string
		status int
		code   string
	}{
		{"对外来源通过", "example.com:39080", "https://example.com:39080", 200, ""},
		{"监听地址通过", "10.0.0.1:39081", "http://10.0.0.1:39081", 200, ""},
		{"无 Origin 的导航请求通过", "example.com:39080", "", 200, ""},
		{"其它 Host 被拒", "evil.example", "https://example.com:39080", 403, "host_denied"},
		{"其它 Origin 被拒", "example.com:39080", "https://evil.example", 403, "origin_denied"},
		{"scheme 不符被拒", "example.com:39080", "http://example.com:39080", 403, "origin_denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 用相对 URL 构造：r.TLS 为 nil，与「反代回源是明文 HTTP」一致。
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("状态码 %d，期望 %d（%s）", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code != "" && !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("错误码应为 %s：%s", tc.code, rec.Body.String())
			}
		})
	}
}

// 未声明来源时不得放宽：这正是「本地用法不变」的可测形式。
func Test未声明来源时只接受监听地址(t *testing.T) {
	s, _, _ := newTestServer(t)
	s.host = "10.0.0.1:39081"

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "example.com:39080"
	req.Header.Set("Origin", "https://example.com:39080")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "host_denied") {
		t.Fatalf("未声明来源时对外 Host 必须被拒：%d %s", rec.Code, rec.Body.String())
	}
}

// Cookie 的 Secure 由「已配置的 HTTPS public origin」决定，而不是由回源
// 连接决定：反代终止 TLS 时 r.TLS 恒为 nil（架构 S09）。
func Test会话Cookie的Secure跟随声明的来源(t *testing.T) {
	cases := []struct {
		name         string
		origin       PublicOrigin
		wantSecure   bool
		wantSameSite string
	}{
		{"https 来源", PublicOrigin{Scheme: "https", Host: "example.com:39080"}, true, "Strict"},
		{"http 来源", PublicOrigin{Scheme: "http", Host: "example.com:39080"}, false, "Strict"},
		{"未声明来源", PublicOrigin{}, false, "Strict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newTestServer(t)
			s.publicOrigin = tc.origin

			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:30142/api/v1/auth", nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("认证失败：%d %s", rec.Code, rec.Body.String())
			}
			cookie := rec.Result().Cookies()
			if len(cookie) == 0 {
				t.Fatal("未下发会话 Cookie")
			}
			if got := strings.Contains(strings.ToLower(rec.Header().Get("Set-Cookie")), "secure"); got != tc.wantSecure {
				t.Fatalf("Secure=%v，期望 %v：%s", got, tc.wantSecure, rec.Header().Get("Set-Cookie"))
			}
			if !cookie[0].HttpOnly {
				t.Fatal("会话 Cookie 必须是 HttpOnly")
			}
			if cookie[0].SameSite != http.SameSiteStrictMode {
				t.Fatalf("SameSite=%v，期望 Strict", cookie[0].SameSite)
			}
		})
	}
}

func Test解析对外来源(t *testing.T) {
	ok := map[string]PublicOrigin{
		"https://example.com:39080": {Scheme: "https", Host: "example.com:39080"},
		"http://10.0.0.1:30142":     {Scheme: "http", Host: "10.0.0.1:30142"},
		"https://example.com":       {Scheme: "https", Host: "example.com"},
		"https://example.com/":      {Scheme: "https", Host: "example.com"},
		"  https://example.com  ":   {Scheme: "https", Host: "example.com"},
		"":                          {},
	}
	for raw, want := range ok {
		got, err := ParsePublicOrigin(raw)
		if err != nil {
			t.Fatalf("%q 应被接受：%v", raw, err)
		}
		if got != want {
			t.Fatalf("%q 解析为 %+v，期望 %+v", raw, got, want)
		}
	}
	bad := []string{
		"example.com",                 // 缺 scheme
		"ftp://example.com",           // scheme 不支持
		"https://example.com/pi",      // 带路径
		"https://example.com?a=b",     // 带查询串
		"https://user:pw@example.com", // 带用户信息
		"https://",                    // 缺主机
	}
	for _, raw := range bad {
		if _, err := ParsePublicOrigin(raw); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
}

// WS 层要与桥自己的来源规则一致：库默认要求 Origin.Host == r.Host，
// 代理改写 Host 时会拒掉桥已经明确允许的来源。
func TestWS来源模式跟随声明的来源(t *testing.T) {
	s, _, _ := newTestServer(t)
	if got := s.wsOriginPatterns(); got != nil {
		t.Fatalf("未声明来源时不应放宽：%v", got)
	}
	s.publicOrigin = PublicOrigin{Scheme: "https", Host: "example.com:39080"}
	got := s.wsOriginPatterns()
	if len(got) != 1 || got[0] != "https://example.com:39080" {
		t.Fatalf("来源模式应为声明的来源：%v", got)
	}
}
