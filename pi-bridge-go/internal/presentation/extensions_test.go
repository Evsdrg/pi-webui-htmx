package presentation

import (
	"strings"
	"testing"
)

func TestDialogFromPi四类方法(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		method string
	}{
		{"select", `{"id":"d1","method":"select","title":"选一个","options":["a","b"]}`, "select"},
		{"confirm", `{"id":"d2","method":"confirm","title":"确定？","message":"会改文件"}`, "confirm"},
		{"input", `{"id":"d3","method":"input","title":"输入","placeholder":"路径"}`, "input"},
		{"editor", `{"id":"d4","method":"editor","title":"编辑","prefill":"旧内容"}`, "editor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := DialogFromPi("", "sess-1", []byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if d.Method != c.method || d.SessionID != "sess-1" {
				t.Fatalf("解析异常: %+v", d)
			}
		})
	}
}

// 目标选择框的选项应给出中文显示文本，但 value 必须保留原文——
// 插件靠比较回传值判定用户选了什么，回传值换成译文会让选择静默失效。
func TestDialogFromPi目标选项汉化但保留回传值(t *testing.T) {
	body := `{"id":"d1","method":"select","title":"Focus open goal","options":["* g1 | running | goal | 整理仓库","Cancel"]}`
	d, err := DialogFromPi("", "s", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.OptionLabels) != len(d.Options) {
		t.Fatalf("显示文本应与选项一一对应: %+v", d)
	}
	if d.Options[0] != "* g1 | running | goal | 整理仓库" {
		t.Fatalf("回传值必须保留原文: %q", d.Options[0])
	}
	if !strings.Contains(d.OptionLabels[0], "进行中") || !strings.Contains(d.OptionLabels[0], "普通目标") {
		t.Fatalf("选项显示文本应汉化: %q", d.OptionLabels[0])
	}
	if d.Options[1] != "Cancel" || d.OptionLabels[1] != "取消" {
		t.Fatalf("固定文案选项：value 保留 Cancel、显示为取消，实际 %q / %q", d.Options[1], d.OptionLabels[1])
	}
}

func TestDialogFromPi拒绝无需回执的方法(t *testing.T) {
	// fire-and-forget 的方法不该出现在待回复列表里，
	// 否则前端会渲染一个永远等不到回执的对话框。
	for _, body := range []string{
		`{"id":"d1","method":"setStatus","statusKey":"k"}`,
		`{"id":"d2","method":"notify","message":"hi"}`,
		`{"id":"d3","method":"setWidget","widgetKey":"k"}`,
		`{"id":"d4","method":"setTitle","title":"t"}`,
		`{"id":"d5","method":"set_editor_text","text":"t"}`,
	} {
		if _, err := DialogFromPi("", "s", []byte(body)); err == nil {
			t.Fatalf("%s 应被拒绝", body)
		}
	}
}

func TestDialogFromPi兜底id(t *testing.T) {
	// 载荷缺 id 时用 URL 里的 id 兜底；两者都缺才报错。
	d, err := DialogFromPi("from-url", "s", []byte(`{"method":"confirm","title":"t"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "from-url" {
		t.Fatalf("应兜底为 from-url，实际 %q", d.ID)
	}
	if _, err := DialogFromPi("", "s", []byte(`{"method":"confirm"}`)); err == nil {
		t.Fatal("id 全缺应报错")
	}
}

func TestDialogFromPi限制选项数量(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"id":"d1","method":"select","options":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"opt`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`"`)
	}
	b.WriteString(`]}`)
	d, err := DialogFromPi("", "s", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Options) != 64 {
		t.Fatalf("选项应截到 64，实际 %d", len(d.Options))
	}
}

func TestDialogFromPi拒绝坏JSON(t *testing.T) {
	if _, err := DialogFromPi("", "s", []byte("坏")); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}
