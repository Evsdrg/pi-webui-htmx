package transport

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pi-bridge-go/internal/presentation"
)

// TestUI方法契约核对静态声明，不替代命令与交互行为测试。
// 联测脚本必须提供真实 UI 包；独立桥仓的 CI 不冒充跨仓验收。
func TestUI方法契约(t *testing.T) {
	dir := os.Getenv("PI_WEBUI_DIR")
	if dir == "" {
		t.Skip("需要 PI_WEBUI_DIR；完整联测请运行 scripts/verify-pair.sh")
	}
	if _, err := presentation.LoadFromDir(dir, SupportedMethods...); err != nil {
		t.Fatalf("UI 包无法由当前桥加载：%v", err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "src", "types", "protocol.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// 此处检查约定的字符串联合类型；格式变化时明确失败，不猜测新语法。
	declaration := regexp.MustCompile(`(?s)export type Method\s*=([^;]+);`).FindSubmatch(source)
	if len(declaration) != 2 {
		t.Fatal("无法找到 Method 的字符串联合声明")
	}
	methods := registeredMethods(t)
	body := strings.TrimSpace(string(declaration[1]))
	for _, part := range strings.Split(strings.TrimPrefix(body, "|"), "|") {
		part = strings.TrimSpace(part)
		if len(part) < 3 || part[0] != '"' || part[len(part)-1] != '"' {
			t.Fatalf("Method 出现未识别语法：%q", part)
		}
		method := part[1 : len(part)-1]
		if !methods[method] {
			t.Errorf("UI 方法未实现或重复声明：%s", method)
		}
		delete(methods, method)
	}
	for method := range methods {
		t.Errorf("桥方法未在 UI 类型声明：%s", method)
	}
}

func registeredMethods(t *testing.T) map[string]bool {
	t.Helper()
	methods := make(map[string]bool, len(SupportedMethods))
	for _, method := range SupportedMethods {
		if methods[method] {
			t.Fatalf("桥方法重复：%s", method)
		}
		methods[method] = true
	}
	return methods
}

func Test方法清单覆盖注册表(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "method-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	methods := registeredMethods(t)
	methodName := regexp.MustCompile("`([a-z_]+(?:\\.[a-z_]+)+)`")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## HTTP") {
			break
		}
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		for _, match := range methodName.FindAllStringSubmatch(strings.Split(line, "|")[1], -1) {
			name := match[1]
			if !methods[name] {
				t.Errorf("清单中出现重复或未注册方法：%s", name)
			}
			delete(methods, name)
		}
	}
	for name := range methods {
		t.Errorf("缺少方法分类：%s", name)
	}
}
