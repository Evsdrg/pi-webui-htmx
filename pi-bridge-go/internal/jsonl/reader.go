// Package jsonl 实现只按 LF 切分、且带大小上限的记录分帧。
package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// ErrTooLarge 表示单条记录超过调用方声明的字节上限。
var ErrTooLarge = errors.New("JSONL 记录超过大小上限")

// ErrIncomplete 表示读到末尾仍没有遇到 LF，即存在不完整的记录。
var ErrIncomplete = errors.New("JSONL 记录在末尾处不完整")

// Read 返回去掉 LF 与可选 CR 之后的记录内容，以及线上实际占用的字节数。
// 即使最后一条记录本身是合法 JSON，只要没有 LF 结尾，也算不完整并报错，
// 因为 Pi 可能还在往文件里追加，不能把半条当成完整历史。
//
// 返回的切片归调用方所有，可以长期保留。需要长期保留字节时用它；
// 只做一次解析就丢掉的批量扫描用 Reusable 更省分配。
func Read(r *bufio.Reader, limit int) ([]byte, int, error) {
	return readInto(nil, r, limit)
}

// Reusable 是复用内部缓冲的分帧读取器。
//
// 它存在的理由是分配量：一次冷扫描里，本包的分帧占了约八成的分配字节——
// 每行都 append 到一个 nil 切片，而超过 bufio 缓冲（默认 4KB）的长行
// 会按倍增反复扩容拷贝。实测 14MB / 2000 轮会话：66601 次分配、28MB。
//
// 代价写在类型名上：**Read 返回的字节在下次 Read 之后就失效**。
// 需要跨行保留字节的调用方必须拷贝，或者继续用包级的 Read。
// 这条约束是有意的——本仓出过同类别名缺陷（子切片交给等待中的 goroutine，
// 读循环随即覆写），而 -race 抓不到它：那是所有权问题，不是数据竞争。
//
// 已核对的调用点：
//   - safe：scan.go、index.go、search.go、metadata.go —— 只在该次迭代内解析
//   - 不适用：pi/client.go（响应与扩展对话框会保留字节）、
//     sessions/lazy.go（把 message 子切片直接返回给调用方）
//
// 内部缓冲一旦增长就不会回收，上界是调用方声明的 limit
// （即单条记录允许的最大字节数）。
type Reusable struct {
	buf []byte
}

// Read 与包级 Read 语义相同，但复用内部缓冲。
// 返回的切片在下一次 Read 调用后被覆写，不得长期保留。
func (u *Reusable) Read(r *bufio.Reader, limit int) ([]byte, int, error) {
	out, n, err := readInto(u.buf[:0], r, limit)
	if out != nil {
		// 记回增长后的缓冲：append 在容量不够时换新数组，
		// 不记的话每次长行都会重新走一遍扩容。
		u.buf = out[:cap(out)]
	}
	return out, n, err
}

// readInto 是 Read 与 Reusable.Read 共用的实现。
// out 作为输出缓冲的起点：nil 表示每次都新分配（Read 的语义），
// 非 nil 表示复用（Reusable 的语义）。
func readInto(out []byte, r *bufio.Reader, limit int) ([]byte, int, error) {
	n := 0
	for {
		part, err := r.ReadSlice('\n')
		n += len(part)
		if n > limit {
			// 超限也返回已读到的内容：调用方需要这段头部定位记录身份，
			// 再把剩余部分丢弃以保持流对齐。
			out = append(out, part...)
			return out, n, ErrTooLarge
		}
		out = append(out, part...)
		if err == nil {
			return bytes.TrimSuffix(out[:len(out)-1], []byte{'\r'}), n, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && n > 0 {
			return nil, n, ErrIncomplete
		}
		return nil, n, err
	}
}

// SkipLine 丢弃当前记录的剩余部分，直到并包括下一个 LF。
// 与 Read 的 ErrTooLarge 分支配合使用：Read 已返回记录头部，
// 这里只负责把行尾吞掉，保证后续帧不会被半条记录污染。
func SkipLine(r *bufio.Reader) error {
	for {
		_, err := r.ReadSlice('\n')
		if err == nil {
			return nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return ErrIncomplete
		}
		return err
	}
}
