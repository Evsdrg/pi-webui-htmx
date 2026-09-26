package workspace

import (
	"bytes"
	"strings"
)

// 图片 MIME 白名单。与惰性工具图片（internal/sessions/lazy.go）保持一致：
// 只收各浏览器都认的通用格式，SVG 不收——它是可执行内容。
var imageMimes = []struct {
	prefix []byte
	mime   string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), "image/png"},
	{[]byte("\xff\xd8\xff"), "image/jpeg"},
	{[]byte("GIF87a"), "image/gif"},
	{[]byte("GIF89a"), "image/gif"},
	{[]byte("BM"), "image/bmp"},
}

// ImageMime 按内容判断图片格式；认不出来返回空串。
// 以魔数为准而不是扩展名——扩展名可以随意改，魔数改不了。
//
// 两种容器布局要分开处理，别混：
//   - RIFF（WebP）：b[0:4]="RIFF"，b[8:12]="WEBP"，b[4:8] 是文件大小
//   - ISO-BMFF（avif/heic）：b[4:8]="ftyp"，b[8:12] 是品牌
//
// 曾经把两者按同一套偏移判断，WebP 因此永远认不出来。
func ImageMime(b []byte, filename string) string {
	for _, cand := range imageMimes {
		if bytes.HasPrefix(b, cand.prefix) {
			return cand.mime
		}
	}
	if len(b) >= 12 {
		if string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
			return "image/webp"
		}
		if string(b[4:8]) == "ftyp" && string(b[8:12]) == "avif" {
			return "image/avif"
		}
	}
	return ""
}

// DetectBinary 判断一段字节是否不该当文本展示。
// 返回空串表示可以按文本读；否则返回给用户看的原因。
//
// 判定用「含 NUL 字节」这个务实标准：文本文件不会有 NUL，
// 而绝大多数二进制格式（图片、压缩包、可执行文件）都很早就会出现。
// 这比维护一份格式清单可靠，也不会把合法的 UTF-8 文本误判成二进制。
func DetectBinary(b []byte, filename string) string {
	if mime := ImageMime(b, filename); mime != "" {
		return "这是图片文件（" + mime + "），请用图片方式查看"
	}
	// 只检查前 8 KB：足够覆盖所有常见格式的魔数，
	// 又不至于为一个超大文件扫完全部内容。
	limit := len(b)
	if limit > 8192 {
		limit = 8192
	}
	if bytes.IndexByte(b[:limit], 0) >= 0 {
		return "这是二进制文件，无法按文本显示"
	}
	// UTF-16 带 BOM，也没有 NUL 之外的文本特征，但仍不是 UTF-8 文本。
	if bytes.HasPrefix(b, []byte("\xff\xfe")) || bytes.HasPrefix(b, []byte("\xfe\xff")) {
		return "这是 UTF-16 编码的文件，无法按 UTF-8 显示"
	}
	return ""
}

// IsAnsiText 判断文本是否含 ANSI 转义序列。
// 用在前端决定要不要挂 ansi_up；这里提供给桥做同类判断（例如导出时）。
func IsAnsiText(text string) bool {
	return strings.Contains(text, "\x1b[")
}
