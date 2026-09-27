package sessions

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"strconv"
	"sync"
)

// entryHead 是一条记录里建索引所需的字段。
// 只为 History 的扫描阶段服务，刻意不做完整解析。
//
// Parent 一分为三是为了把下游的 JSON 处理全部消掉：
// 曾经 Parent 保留原始文本，调用方再 json.Unmarshal 一次——
// 剖析显示那一次解析占了 History 一半以上 CPU，还每次都分配一个 []byte。
type entryHead struct {
	Type string
	ID   string
	// HasParent 表示 parentId 成员存在。缺失是格式错误，与显式 null 不同。
	HasParent bool
	// ParentNull 表示 parentId 显式为 null，即没有父亲（会话头之后的第一条）。
	ParentNull bool
	// Parent 是已解引用的父条目 ID；ParentNull 或 HasParent 为假时为空。
	Parent string
}

// headDecoder 把解码器与它的字节读器配成一对，便于整对入池复用。
//
// 为什么必须复用：jsontext.NewDecoder 每次要分配解码器状态。
// 实测复用后单个 112 KB 行从 661 ns 降到 156 ns，小行也不再倒贴——
// 不复用时小行反而比整体 Unmarshal 慢（每行多出十余次分配）。
type headDecoder struct {
	reader *bytes.Reader
	dec    *jsontext.Decoder
}

var headPool = sync.Pool{New: func() any {
	r := &bytes.Reader{}
	return &headDecoder{reader: r, dec: jsontext.NewDecoder(r)}
}}

// parseEntryHead 只读一条记录的行首字段，不解析整行。
//
// 为什么值得单独写：History 每次请求都要扫整个会话文件，而扫描阶段
// 只需要 type/id/parentId。实测真实会话单行最大 112 KB（一条带长思考块的
// assistant 消息），整体 Unmarshal 到结构体仍要把这 112 KB 的字符串完整
// 扫过去——剖析显示 59.9% 的 CPU 花在 json.Unmarshal，其中近一半是
// ConsumeStringResumable。这三字段通常在行首约 120 字节内。
//
// 返回 ok=false 表示这条记录不适合快路径（字段没找齐、类型不对、JSON 异常），
// 调用方应回退到完整 Unmarshal，由它给出权威结果与错误。
func parseEntryHead(b []byte) (entryHead, bool) {
	h := headPool.Get().(*headDecoder)
	defer headPool.Put(h)
	h.reader.Reset(b)
	dec := h.dec
	dec.Reset(h.reader)

	// 第一条 token 必须是对象左括号。
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return entryHead{}, false
	}
	var head entryHead
	var haveType, haveID bool
	for {
		kind := dec.PeekKind()
		if kind == 0 || kind == '}' {
			break
		}
		name, err := dec.ReadToken()
		if err != nil {
			return entryHead{}, false
		}
		key := name.String() // Token 会被后续调用作废，必须立即取出
		value, err := dec.ReadValue()
		if err != nil {
			return entryHead{}, false
		}
		switch key {
		case "type":
			if value.Kind() != '"' {
				return entryHead{}, false
			}
			head.Type = unquoteJSON(value.String())
			haveType = true
		case "id":
			if value.Kind() != '"' {
				return entryHead{}, false
			}
			head.ID = unquoteJSON(value.String())
			haveID = true
		case "parentId":
			head.HasParent = true
			switch value.Kind() {
			case 'n': // null
				head.ParentNull = true
			case '"':
				head.Parent = unquoteJSON(value.String())
			default:
				// 数字、对象、布尔都让慢路径去报错，这里不猜语义。
				return entryHead{}, false
			}
		default:
			// 值已经在上面 ReadValue 读走了，这里不能再 SkipValue——
			// 那会跳到下一个成员去，把整个解析搅乱。
			_ = value
		}
		// 三个都拿到就停。剩下的（通常是巨大的 message）不再扫描。
		if haveType && haveID && head.HasParent {
			return head, true
		}
	}
	// 走完还没找齐：字段顺序异常或有缺失，交给慢路径裁决。
	if !haveType || !haveID || !head.HasParent {
		return entryHead{}, false
	}
	return head, true
}

// unquoteJSON 去掉 JSON 字符串 token 的引号并还原转义。
//
// 必须用 strconv.Unquote 而不是 json.Unmarshal：后者每次都要分配一个
// []byte 并跑一遍完整 JSON 解析。两种转义语法几乎重合，唯一差异是
// JSON 的 \/ 在 Go 里非法，所以失败时回退到 json.Unmarshal 兜底——
// Pi 的 ID 与 type 不含转义，走不到回退。
func unquoteJSON(raw string) string {
	if len(raw) < 2 || raw[0] != '"' {
		return raw
	}
	if out, err := strconv.Unquote(raw); err == nil {
		return out
	}
	var out string
	if json.Unmarshal([]byte(raw), &out) != nil {
		return raw
	}
	return out
}

// balancedJSON 用括号配平粗校验一条记录是不是结构完整的 JSON 对象。
//
// 为什么需要它：快路径只读行首三个字段就停，剩下的大块 message 根本不看。
// 于是一条「type/id/parentId 齐全、但正文被写坏」的记录会被静默接受——
// 与「完整损坏行显式报错」的契约不符（B13）。
//
// 只统计括号深度，不进字符串内容：成本是 O(n) 的单次扫描、零分配，
// 与完整 Unmarshal 相比可以忽略。它抓不到「括号配平但值类型错误」，
// 那类仍由慢路径裁决；这里只堵住「结构不完整」这一大类。
func balancedJSON(b []byte) bool {
	depth := 0
	inString := false
	escaped := false
	for _, c := range b {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !inString && !escaped
}
