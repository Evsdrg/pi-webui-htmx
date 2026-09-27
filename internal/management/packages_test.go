package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeSettings(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeInstalled(t *testing.T, agentDir, name, version string) {
	t.Helper()
	// Pi 0.85.1 的 getManagedNpmInstallPath 使用 agentDir/npm/node_modules。
	dir := filepath.Join(agentDir, "npm", "node_modules", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": name, "version": version})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
}

// withRegistry 把被测客户端指向本地 registry，避免测试访问公网。
func withRegistry(c *Config, srv *httptest.Server) {
	c.httpClient = srv.Client()
	c.registryBaseURL = srv.URL
}

func TestPackages列出字符串与对象形式(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b@1.0.0",{"source":"npm:@c/d","disabled":true}]}`)
	c := NewConfig(dir, DefaultLimits())
	list, err := c.Packages(context.Background(), DiscoveryLimits{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应列出两个包: %+v", list)
	}
	byName := map[string]PackageInfo{}
	for _, p := range list {
		byName[p.Name] = p
	}
	if byName["@a/b"].Source != "npm:@a/b@1.0.0" {
		t.Fatalf("字符串形式解析异常: %+v", byName["@a/b"])
	}
	if byName["@c/d"].Source != "npm:@c/d" || !byName["@c/d"].Disabled {
		t.Fatalf("对象形式解析异常: %+v", byName["@c/d"])
	}
}

func TestPackages去重(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b","npm:@a/b"]}`)
	c := NewConfig(dir, DefaultLimits())
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("重复来源应去重: %+v", list)
	}
}

func TestPackages版本比对(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b","npm:@c/d"]}`)
	writeInstalled(t, dir, "@a/b", "1.0.0")
	writeInstalled(t, dir, "@c/d", "2.0.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 依路径返回不同最新版本。
		if filepath.Base(r.URL.Path) == "b" {
			_, _ = w.Write([]byte(`{"version":"1.5.0"}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	defer srv.Close()

	c := NewConfig(dir, DefaultLimits())
	withRegistry(c, srv)
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]PackageInfo{}
	for _, p := range list {
		byName[p.Name] = p
	}
	if byName["@a/b"].Version != "1.0.0" || byName["@c/d"].Version != "2.0.0" {
		t.Fatalf("已安装版本读取异常: %+v", list)
	}
	if !byName["@a/b"].HasUpdate || byName["@c/d"].HasUpdate {
		t.Fatalf("语义版本比较异常: %+v", list)
	}
	for _, p := range list {
		if p.Error != "" {
			t.Fatalf("查询成功不应残留错误: %+v", p)
		}
	}
}

// TestPackages查询失败仍返回列表 覆盖 registry 不可达：只影响 latest 字段。
func TestPackages查询失败仍返回列表(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b"]}`)
	writeInstalled(t, dir, "@a/b", "1.0.0")
	c := NewConfig(dir, DefaultLimits())
	withRegistry(c, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Latest != "" {
		t.Fatalf("registry 失败不应给出 latest: %+v", list)
	}
	if list[0].Error == "" {
		t.Fatalf("应记录失败原因: %+v", list[0])
	}
	if list[0].HasUpdate {
		t.Fatalf("拿不到 latest 时不得判定有更新: %+v", list[0])
	}
}

// TestPackages无法确认已安装版本 覆盖 agentDir/npm 缺失：不猜旧式全局目录。
func TestPackages无法确认已安装版本(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b"]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	defer srv.Close()
	c := NewConfig(dir, DefaultLimits())
	withRegistry(c, srv)
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Version != "" || list[0].HasUpdate {
		t.Fatalf("未确认已安装版本不得判定更新: %+v", list[0])
	}
	if list[0].Error == "" {
		t.Fatalf("应说明版本无法确认: %+v", list[0])
	}
}

// TestPackages受限来源不查询 覆盖 alias/file/git 形式：不当成 registry 包。
func TestPackages受限来源不查询(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["npm:@a/b@npm:@c/d","npm:@a/b@file:../x","git:github.com/u/r@v1"]}`)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	defer srv.Close()
	c := NewConfig(dir, DefaultLimits())
	withRegistry(c, srv)
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("受限来源不应查询 registry: %d", requests)
	}
	// git/本地来源没有可比的 registry 版本，静默跳过而非报错。
	for _, p := range list {
		if strings.HasPrefix(p.Source, "npm:") && p.Error == "" {
			t.Fatalf("npm alias/file 指定方式应被拒绝: %+v", p)
		}
	}
}

// TestPackages条目数与查询并发上限 覆盖大量条目不产生同量请求。
func TestPackages条目数与查询并发上限(t *testing.T) {
	dir := t.TempDir()
	entries := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		entries = append(entries, fmt.Sprintf("npm:pkg%02d", i))
	}
	writeSettings(t, dir, `{"packages":`+mustJSON(entries)+`}`)
	var active, peak int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	defer srv.Close()
	c := NewConfig(dir, DefaultLimits())
	withRegistry(c, srv)
	if _, err := c.Packages(context.Background(), DiscoveryLimits{}); err != nil {
		t.Fatal(err)
	}
	if peak > 4 {
		t.Fatalf("registry 并发超过上限: %d", peak)
	}
}

func mustJSON(v any) string {
	body, _ := json.Marshal(v)
	return string(body)
}

func TestPackages本地路径不查registry(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, `{"packages":["/opt/local/ext","./rel/ext"]}`)
	c := NewConfig(dir, DefaultLimits())
	list, err := c.Packages(context.Background(), DiscoveryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Error != "" {
			t.Fatalf("本地路径不应查询 registry: %+v", p)
		}
	}
	if len(list) != 2 {
		t.Fatalf("应列出两个本地来源: %+v", list)
	}
}

func TestPackageDisplayName(t *testing.T) {
	cases := map[string]string{
		"npm:@a/b@1.2.3": "@a/b",
		"npm:plain":      "plain",
		"/opt/local":     "/opt/local",
		"npm:@a/b":       "@a/b",
	}
	for in, want := range cases {
		if got := packageDisplayName(in); got != want {
			t.Fatalf("%q -> %q，期望 %q", in, got, want)
		}
	}
}
