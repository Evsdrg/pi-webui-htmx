package pi

import (
	"encoding/base64"
	"strconv"
	"strings"

	"pi-bridge-go/internal/protocol"
)

// 附件上限。图片以 base64 内联进 RPC 帧，过大的图会让单帧超出 Pi 的
// 读取缓冲，也会把 WS 单帧上限顶满。
const (
	MaxImages       = 8
	MaxImageBytes   = 8 << 20  // 单张解码后上限
	MaxImageDataLen = 12 << 20 // 单张 base64 文本上限（解码后约 8 MB）
)

// allowedImageTypes 是允许内联的图片 MIME 白名单。
// 只收各供应商都认的通用格式；SVG 不收——它是可执行内容，
// 且多数供应商不认。
var allowedImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// Image 是一条内联图片附件。
type Image struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// DecodeImages 校验并规范化前端传来的图片数组。
//
// 前端可能传 data URL（"data:image/png;base64,...."）或裸 base64 + 独立
// mimeType 字段；两种都接受，输出统一成 Pi 的 ImageContent 形状。
// 全部校验在这里做，不让未校验内容到达 Pi。
func DecodeImages(raw []Image) ([]map[string]any, error) {
	if len(raw) > MaxImages {
		return nil, protocol.E("limit_exceeded", "单条消息最多附带 "+strconv.Itoa(MaxImages)+" 张图片")
	}
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		data := strings.TrimSpace(item.Data)
		if data == "" {
			return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片没有数据")
		}
		if len(data) > MaxImageDataLen {
			return nil, protocol.E("limit_exceeded", "第 "+strconv.Itoa(i+1)+" 张图片超过体积上限")
		}
		mime := strings.TrimSpace(item.MimeType)
		// data URL：从头部取 MIME，并剥掉前缀只留 base64。
		if strings.HasPrefix(data, "data:") {
			at := strings.Index(data, ",")
			if at < 0 {
				return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片的 data URL 不完整")
			}
			head := data[len("data:"):at]
			if semi := strings.Index(head, ";"); semi >= 0 {
				if mime == "" {
					mime = head[:semi]
				}
				head = head[semi+1:]
			}
			if !strings.Contains(strings.ToLower(head), "base64") {
				return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片必须是 base64 编码")
			}
			data = strings.TrimSpace(data[at+1:])
		}
		if mime == "" {
			return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片缺少 mimeType")
		}
		mime = strings.ToLower(strings.SplitN(mime, ";", 2)[0])
		if !allowedImageTypes[mime] {
			return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片的格式不受支持: "+mime)
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			// 兼容 URL-safe 变体与缺少填充的实现。
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
			if err != nil {
				return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片不是合法 base64")
			}
		}
		if len(decoded) == 0 {
			return nil, protocol.E("invalid_params", "第 "+strconv.Itoa(i+1)+" 张图片内容为空")
		}
		if len(decoded) > MaxImageBytes {
			return nil, protocol.E("limit_exceeded", "第 "+strconv.Itoa(i+1)+" 张图片超过体积上限")
		}
		out = append(out, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(decoded), "mimeType": mime})
	}
	return out, nil
}
