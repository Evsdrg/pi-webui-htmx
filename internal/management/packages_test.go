package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeInstalled(t *testing.T, agentDir, name, version string) {
	t.Helper()
	dir := filepath.Join(agentDir, "node_modules", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": name, "version": version})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
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
	// registry 地址写死为 npmjs，这里直接测 installedVersion 与比对逻辑。
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
	// registry 不可达时只影响 latest 字段，不应让列表失败。
	for _, p := range list {
		if p.Error == "" {
			t.Fatalf("registry 不可达时应记录错误: %+v", p)
		}
		if p.HasUpdate {
			t.Fatalf("拿不到 latest 时不得判定有更新: %+v", p)
		}
	}
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
