package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRead按LF切分且接受CRLF(t *testing.T) {
	// 第三条故意不带 LF：Unicode 分隔符不是边界，半行必须判不完整。
	r := bufio.NewReader(strings.NewReader("{\"a\":1}\n{\"b\":\"line\u2028sep\"}\r\n{\"c\":3}"))
	b1, n1, err := Read(r, 1024)
	if err != nil || string(b1) != `{"a":1}` || n1 != 8 {
		t.Fatalf("第一条记录异常: %q %d %v", b1, n1, err)
	}
	// JSON 字符串里直接放 U+2028 原字符：它不是记录分隔符。
	want2 := "{\"b\":\"line\u2028sep\"}"
	b2, _, err := Read(r, 1024)
	if err != nil || string(b2) != want2 {
		t.Fatalf("Unicode 分隔符被误切分: %q %v", b2, err)
	}
	b3, _, err := Read(r, 1024)
	if !errors.Is(err, ErrIncomplete) || len(b3) != 0 {
		t.Fatalf("末条无 LF 应报 ErrIncomplete: %q %v", b3, err)
	}
}

func TestRead完整结尾返回EOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("{\"a\":1}\n"))
	if b, _, err := Read(r, 1024); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("完整记录读取失败: %q %v", b, err)
	}
	if _, _, err := Read(r, 1024); !errors.Is(err, io.EOF) {
		t.Fatalf("读完后应报 EOF，实际 %v", err)
	}
}

func TestRead超限报错(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader(append(bytes.Repeat([]byte("x"), 500), '\n')))
	if _, _, err := Read(r, 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("应报 ErrTooLarge，实际 %v", err)
	}
}

func TestRead跨缓冲区读取(t *testing.T) {
	long := strings.Repeat("y", 4096)
	r := bufio.NewReader(strings.NewReader(`{"pad":"` + long + "\"}\n"))
	b, _, err := Read(r, 8192)
	if err != nil || len(b) != len(`{"pad":"`+long+"\"}") {
		t.Fatalf("跨缓冲区长记录读取失败: %d %v", len(b), err)
	}
}
