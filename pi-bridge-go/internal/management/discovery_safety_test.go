package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func Test供应商重定向不携带凭据跟随(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	_, err := newDiscoveryConfig(t).Discover(context.Background(), source.URL, "anthropic-messages", "secret-provider", map[string]string{"X-Custom": "secret-header"}, DefaultDiscoveryLimits())
	if forwarded.Load() != 0 {
		t.Fatal("重定向目标收到了带凭据的请求")
	}
	if err == nil {
		t.Fatal("重定向未明确报错")
	}
}

func Test供应商超限响应不能当完整JSON接受(t *testing.T) {
	body := `{"data":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body+strings.Repeat(" ", 100))
	}))
	defer server.Close()
	limits := DefaultDiscoveryLimits()
	limits.MaxBytes = int64(len(body))
	_, err := newDiscoveryConfig(t).Discover(context.Background(), server.URL, "", "", nil, limits)
	if err == nil {
		t.Fatal("超限正文被静默截为合法 JSON")
	}
}

func Test供应商错误正文不回显秘密(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer server.Close()
	_, err := newDiscoveryConfig(t).Discover(context.Background(), server.URL, "", "secret-never-log", nil, DefaultDiscoveryLimits())
	if err == nil || strings.Contains(err.Error(), "secret-never-log") {
		t.Fatalf("错误未脱敏：%v", err)
	}
}
