package presentation

import "testing"

// DocBase 决定外壳的 <base href>，也就是浏览器解析一切相对 URL 的基准。
// 它直接来自隧道帧（relay 构造），所以必须只接受纯路径形态：
// 任何协议、主机、协议相对写法或 `..` 都能把资源请求引到外部域，
// 等于让外壳去加载别人的脚本。
func Test文档基地址只接受纯路径(t *testing.T) {
	cases := []struct {
		mount string
		want  string
		why   string
	}{
		{"", "/", "没有前缀（本地直连）就是根"},
		{"/", "/", "显式的根"},
		{"/d/dev-1", "/d/dev-1/", "设备前缀要补上结尾斜杠"},
		{"/d/dev-1/", "/d/dev-1/", "已有结尾斜杠"},
		{"/a/b/c", "/a/b/c/", "多级前缀"},
		{"d/dev-1", "/", "不是绝对路径，拒绝"},
		{"//evil.example", "/", "协议相对写法会把请求引到外部域"},
		{"https://evil.example/d/x", "/", "带协议会覆盖基地址"},
		{"/d/../../x", "/", "`..` 能爬出前缀"},
		{"/d/dev 1", "/", "空格等非法字符拒绝"},
		{"/d/dev-1:8080", "/", "冒号拒绝"},
		{"/d/<script>", "/", "尖括号拒绝"},
		{"  /d/dev-1  ", "/d/dev-1/", "两端空白先剥掉"},
	}
	for _, c := range cases {
		if got := DocBase(c.mount); got != c.want {
			t.Errorf("DocBase(%q) = %q，期望 %q（%s）", c.mount, got, c.want, c.why)
		}
	}
}
