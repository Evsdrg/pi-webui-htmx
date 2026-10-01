package sessions

import (
	"encoding/json"
	"testing"
)

// 三种形状在三个调用点上必须给出与旧实现相同的结果。
func Test形状判定与原语义一致(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want jsonShape
	}{
		{"字符串", `"纯文本"`, shapeString},
		{"带前导空白", "  \n\t[{\"type\":\"text\"}]", shapeArray},
		{"块数组", `[{"type":"text","text":"a"}]`, shapeArray},
		{"对象", `{"role":"user"}`, shapeObject},
		{"null", `null`, shapeUnknown},
		{"空", ``, shapeUnknown},
	}
	for _, c := range cases {
		if got := shapeOf([]byte(c.raw)); got != c.want {
			t.Errorf("%s：形状应为 %v，得到 %v", c.name, c.want, got)
		}
	}
}

func Test压平内容按形状分支(t *testing.T) {
	if got := flattenContent(json.RawMessage(`"一句话"`)); got != "一句话" {
		t.Errorf("字符串内容应原样返回，得到 %q", got)
	}
	if got := flattenContent(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "a b" {
		t.Errorf("块数组应以空格连接，得到 %q", got)
	}
	// null / 对象 / 空值都压不出文本，且不得因「解析失败」而误判成另一种形状。
	for _, raw := range []string{`null`, `{"role":"user"}`, ``} {
		if got := flattenContent(json.RawMessage(raw)); got != "" {
			t.Errorf("%s 应返回空串，得到 %q", raw, got)
		}
	}
}

func Test块扫描按形状分支(t *testing.T) {
	// 纯文本（字符串）内容没有可延后加载的块。
	if got := scanLazyBlocks(json.RawMessage(`{"role":"assistant","content":"纯文本"}`)); len(got) != 0 {
		t.Fatalf("字符串内容不应产生惰性块: %+v", got)
	}
	blocks := scanLazyBlocks(json.RawMessage(`{"role":"assistant","content":[{"type":"thinking","thinking":"x"}]}`))
	if len(blocks) != 1 || blocks[0].Kind != "thinking" {
		t.Fatalf("思考块应被识别: %+v", blocks)
	}
	blocks = scanLazyBlocks(json.RawMessage(`{"role":"user","content":[{"type":"image","data":"x"}]}`))
	if len(blocks) != 1 || blocks[0].Kind != "image" {
		t.Fatalf("用户图片应被识别: %+v", blocks)
	}
}
