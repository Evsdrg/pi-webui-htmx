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
func Read(r *bufio.Reader, limit int) ([]byte, int, error) {
	var out []byte
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
