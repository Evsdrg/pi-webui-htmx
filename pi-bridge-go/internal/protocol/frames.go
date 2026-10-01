package protocol

// 本文件是帧预算的唯一真相源：浏览器单帧上限，以及所有读这一帧的地方
// 必须使用的同一个数字。

// 附件预算与帧上限是一组数字。
//
// 浏览器发的最大单帧就是「附件上限 + 封套」，桥上读这一帧的每一处
// （直连 WS、隧道客户端、relay 的转发器）都必须按同一个值放行。
// 任何一处更小气，合法请求都会被当成超限帧处理——网关层的表现是
// **断开整条连接**，用户看到的是「发图片就掉线」（B53：relay 与隧道
// 客户端各自写死 1 MiB，正好落在图片附件的体积区间里）。
const (
	// MaxImageAttachments 是单条消息最多携带的图片数。
	MaxImageAttachments = 8
	// MaxImageAttachmentBytes 是单张图片 base64 文本的上限（解码后约 8 MB）。
	MaxImageAttachmentBytes = 12 << 20
	// FrameEnvelopeBytes 给 JSON 封套、请求字段与转义留的余量。
	FrameEnvelopeBytes = 1 << 20

	// BrowserFrameLimit 是浏览器单帧的最大合法字节数。
	BrowserFrameLimit = MaxImageAttachments*MaxImageAttachmentBytes + FrameEnvelopeBytes

	// RelayEnvelopeBytes 是隧道/relay 转发时外面那层路由封装的开销上限。
	// 封装只加固定前缀（{"to"/"from":"<最多 64 字符的 clientId>","data":…}），
	// 转发器不再做 HTML 转义，所以膨胀不会随载荷放大。
	RelayEnvelopeBytes = 256
)
