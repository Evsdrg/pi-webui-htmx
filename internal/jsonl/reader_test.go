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

// Reusable 的复用必须真的生效，而且不得把内容带出上一次调用。
func TestReusable复用缓冲且不跨界泄漏(t *testing.T) {
	line1 := strings.Repeat("a", 9000) + "\n" // 超过 bufio 默认缓冲，会走扩容分支
	line2 := "second\n"
	r := bufio.NewReader(strings.NewReader(line1 + line2))
	var u Reusable

	first, n, err := u.Read(r, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 9000 || n != 9001 {
		t.Fatalf("首行长度错误: len=%d n=%d", len(first), n)
	}
	capacityAfterFirst := cap(u.buf)
	// 下一次读取会覆写同一块缓冲，因此这里先自行拷一份做对照。
	wantFirst := string(first)

	second, _, err := u.Read(r, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "second" {
		t.Fatalf("第二行内容错误: %q", second)
	}
	if cap(u.buf) < capacityAfterFirst {
		t.Fatalf("缓冲不应缩小: %d < %d", cap(u.buf), capacityAfterFirst)
	}
	// 真正要钉住的契约：第一行的字节已经不保证有效（可能被覆写）。
	// 这里断言的是「第二行内容正确且长度等于第二行」——若实现把两行拼在
	// 一起（忘记从 buf[:0] 开始），长度就会是 9007。
	if len(second) != len("second") {
		t.Fatalf("第二行长度应为 %d，得到 %d（缓冲未从头复用）", len("second"), len(second))
	}
	_ = wantFirst
}

// 复用缓冲下，超限记录仍要返回已读到的头部，供调用方定位记录身份并跳过。
func TestReusable超限返回头部(t *testing.T) {
	content := strings.Repeat("x", 5000)
	r := bufio.NewReader(strings.NewReader(content + "\n" + "tail\n"))
	var u Reusable
	out, n, err := u.Read(r, 1000)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("应报 ErrTooLarge，得到 %v", err)
	}
	if len(out) == 0 || n <= 1000 {
		t.Fatalf("超限时应返回已读头部: len=%d n=%d", len(out), n)
	}
	if err := SkipLine(r); err != nil {
		t.Fatal(err)
	}
	next, _, err := u.Read(r, 1000)
	if err != nil || string(next) != "tail" {
		t.Fatalf("跳过后应能继续读: %q %v", next, err)
	}
}
