package pi

import (
	"encoding/base64"
	"strings"
	"testing"
)

func png() string {
	return base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n-fake-png-bytes"))
}

func TestDecodeImages接受裸base64与显式mime(t *testing.T) {
	out, err := DecodeImages([]Image{{Type: "image", Data: png(), MimeType: "image/png"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应返回 1 张: %v", out)
	}
	if out[0]["mimeType"] != "image/png" || out[0]["type"] != "image" {
		t.Fatalf("形状不符 Pi 的 ImageContent: %v", out[0])
	}
	if out[0]["data"].(string) != png() {
		t.Fatal("base64 未被规范化")
	}
}

func TestDecodeImages接受dataURL并剥前缀(t *testing.T) {
	out, err := DecodeImages([]Image{{Data: "data:image/jpeg;base64," + png()}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["mimeType"] != "image/jpeg" {
		t.Fatalf("未从 data URL 取到 MIME: %v", out[0])
	}
	if strings.HasPrefix(out[0]["data"].(string), "data:") {
		t.Fatal("data URL 前缀没有被剥掉")
	}
}

func TestDecodeImages拒绝不受支持的格式(t *testing.T) {
	// SVG 不收：它是可执行内容，且多数供应商不认。
	for _, mime := range []string{"image/svg+xml", "application/pdf", "text/plain", ""} {
		if _, err := DecodeImages([]Image{{Data: png(), MimeType: mime}}); err == nil {
			t.Errorf("mime %q 应被拒绝", mime)
		}
	}
}

func TestDecodeImages拒绝非法base64与空数据(t *testing.T) {
	for _, bad := range []Image{
		{Data: "!!!not-base64!!!", MimeType: "image/png"},
		{Data: "", MimeType: "image/png"},
		{Data: "data:image/png,notbase64", MimeType: "image/png"},
	} {
		if _, err := DecodeImages([]Image{bad}); err == nil {
			t.Errorf("%+v 应被拒绝", bad)
		}
	}
}

func TestDecodeImages拒绝超量与超大图(t *testing.T) {
	many := make([]Image, MaxImages+1)
	for i := range many {
		many[i] = Image{Data: png(), MimeType: "image/png"}
	}
	if _, err := DecodeImages(many); err == nil {
		t.Fatal("超过张数上限应被拒绝")
	}
	huge := base64.StdEncoding.EncodeToString(make([]byte, MaxImageBytes+1024))
	if _, err := DecodeImages([]Image{{Data: huge, MimeType: "image/png"}}); err == nil {
		t.Fatal("超过体积上限应被拒绝")
	}
	// base64 文本本身超长也要先拦，避免先解码进内存。
	long := strings.Repeat("A", MaxImageDataLen+1)
	if _, err := DecodeImages([]Image{{Data: long, MimeType: "image/png"}}); err == nil {
		t.Fatal("超长 base64 文本应被拒绝")
	}
}

func TestDecodeImages空数组返回空结果(t *testing.T) {
	out, err := DecodeImages(nil)
	if err != nil {
		t.Fatal(err)
	}
	// messageFields 靠 len(images) 判断是否带 images 键，
	// 所以空数组必须长度为 0（值可以是空切片）。
	if len(out) != 0 {
		t.Fatalf("空数组应返回空结果: %v", out)
	}
}
