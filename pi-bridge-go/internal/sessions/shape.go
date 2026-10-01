package sessions

// jsonShape 是一段 JSON 值的首字节形状。
//
// 为什么需要它：content 既可能是字符串（纯文本消息），也可能是数组（块消息）。
// 旧写法靠「试一次、失败再试另一种」，把 unstarshall 的失败当类型判断用——
// 而解码失败会构造带位置的错误对象（渲染基准里这类对象约 9KB/次），
// 而且「形状不符」与「内容损坏」被混成同一种结果。首字节判断一次就够。
type jsonShape uint8

const (
	shapeUnknown jsonShape = iota
	shapeString
	shapeArray
	shapeObject
)

// shapeOf 看首个非空白字节判断形状；空值与 null 都返回 shapeUnknown。
// 它只回答「该按哪种形状去解析」，不校验内容是否合法——
// 解析失败仍由各自的 unmarshal 结果决定。
func shapeOf(raw []byte) jsonShape {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return shapeString
		case '[':
			return shapeArray
		case '{':
			return shapeObject
		default:
			return shapeUnknown
		}
	}
	return shapeUnknown
}
