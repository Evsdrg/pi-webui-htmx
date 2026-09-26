package workspace

import (
	"bytes"
	"strings"
	"testing"
)

func pngBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01, 0x02, 0x03}, 64)...)
}

func TestImageMime按魔数判定(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"png", pngBytes(), "image/png"},
		{"jpeg", append([]byte("\xff\xd8\xff"), 0x10, 0x20), "image/jpeg"},
		{"gif87", append([]byte("GIF87a"), 0x01), "image/gif"},
		{"gif89", append([]byte("GIF89a"), 0x01), "image/gif"},
		{"bmp", append([]byte("BM"), bytes.Repeat([]byte{0}, 30)...), "image/bmp"},
		{"webp", append(append([]byte("RIFF"), 0, 0, 0, 0), append([]byte("WEBP"), 0, 0)...), "image/webp"},
		{"avif", append(append(append([]byte{0, 0, 0, 0x18}, []byte("ftyp")...), []byte("avif")...), 0, 0), "image/avif"},
		{"纯文本", []byte("hello"), ""},
		{"扩展名撒谎的文本", []byte("not really a png"), ""},
	}
	for _, c := range cases {
		if got := ImageMime(c.body); got != c.want {
			t.Errorf("%s: 期望 %q，实际 %q", c.name, c.want, got)
		}
	}
}

func TestDetectBinary区分文本图片与二进制(t *testing.T) {
	// 图片必须被识别出来，并提示走图片路径。
	if got := DetectBinary(pngBytes()); got == "" || !strings.Contains(got, "图片") {
		t.Errorf("PNG 应被识别为图片: %q", got)
	}
	// 含 NUL 的二进制。
	if got := DetectBinary([]byte("abc\x00def")); got == "" {
		t.Error("含 NUL 应判为二进制")
	}
	// UTF-16 BOM。
	if got := DetectBinary([]byte("\xff\xfea\x00b\x00")); got == "" {
		t.Error("UTF-16 应被拒绝")
	}
	// 正常文本（含中文、emoji、制表符、CRLF）不得误判。
	for _, text := range []string{"hello", "中文内容", "a\tb\r\nc", "🎉 emoji", strings.Repeat("x", 100000)} {
		if got := DetectBinary([]byte(text)); got != "" {
			t.Errorf("文本 %q 被误判为二进制: %s", text[:min(20, len(text))], got)
		}
	}
	// NUL 出现在 8 KB 之后不算——只扫头部，避免为大文件付全量扫描成本。
	late := append(bytes.Repeat([]byte("a"), 9000), 0)
	if got := DetectBinary(late); got != "" {
		t.Errorf("8 KB 之后的 NUL 不该被扫到: %s", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
