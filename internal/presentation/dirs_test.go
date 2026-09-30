package presentation

import (
	"strings"
	"testing"
)

// 目录选择器只列子目录，且每行都要带上完整路径——前端靠它发起下一次
// 导航，只给名字的话点击后无法知道去了哪里。
func Test目录选择器只列子目录且带完整路径(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderDirs("/srv/projects", "/opt", []DirRow{
		{Name: "pi", Path: "/srv/projects/pi"},
		{Name: "zcode", Path: "/srv/projects/zcode"},
	}, false)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{
		// 相对路径：设备前缀部署（relay 的 /d/{id}/）下，绝对路径会打到
		// 根而不是设备前缀，所以模板里一律写相对（B54）。
		`hx-get="ui/dirs?path=/srv/projects/pi"`,
		`hx-get="ui/dirs?path=/srv/projects/zcode"`,
		`hx-target="#dir-list"`,
		`>pi<`, `>zcode<`,
		// 当前路径用带外交换写出，供「使用此目录」读取
		`id="dir-current"`, `value="/srv/projects"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("应包含 %s：%s", want, html)
		}
	}
}

// 没有父目录（当前就是工作区根）时不能渲染「上一级」：桥只允许在根内
// 浏览，给出一个越出根的按钮会诱导用户点到沙箱外。
func Test位于根时不提供上一级(t *testing.T) {
	renderer := testRenderer(t)
	html, err := renderer.RenderDirs("/srv/projects", "", []DirRow{
		{Name: "pi", Path: "/srv/projects/pi"},
	}, false)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(html, "dir-up") {
		t.Fatalf("位于根时不应有上一级按钮：%s", html)
	}
	// 没有子目录与截断都要明说，不能渲染成空白让人以为加载失败。
	empty, err := renderer.RenderDirs("/srv/projects", "", nil, false)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(empty, "没有子目录") {
		t.Fatalf("空目录应有提示：%s", empty)
	}
	truncated, err := renderer.RenderDirs("/srv/projects", "", nil, true)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(truncated, "目录过多") {
		t.Fatalf("截断应有提示：%s", truncated)
	}
}
