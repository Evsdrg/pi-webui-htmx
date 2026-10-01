package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/testutil"
)

// fakePipe 是一段可控的管道：写入被记录，读取可阻塞到测试注入数据为止。
type fakePipe struct {
	mu      sync.Mutex
	written []byte
	order   []string
	pending []byte
	closed  bool
	ready   chan struct{}
	// gate 非空时 Write 会阻塞，用于模拟 Pi 不读 stdin 的背压场景。
	gate chan struct{}
}

func newFakePipe() *fakePipe { return &fakePipe{ready: make(chan struct{}, 1)} }

// blockWrites 让后续写入阻塞，直到 releaseWrites 被调用。
func (f *fakePipe) blockWrites() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate == nil {
		f.gate = make(chan struct{})
	}
}

func (f *fakePipe) releaseWrites() {
	f.mu.Lock()
	gate := f.gate
	f.gate = nil
	f.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (f *fakePipe) Write(p []byte) (int, error) {
	f.mu.Lock()
	gate, closed := f.gate, f.closed
	f.mu.Unlock()
	if gate != nil {
		<-gate
		f.mu.Lock()
		closed = f.closed
		f.mu.Unlock()
	}
	if closed {
		return 0, io.ErrClosedPipe
	}
	f.mu.Lock()
	f.order = append(f.order, string(p))
	f.written = append(f.written, p...)
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakePipe) Read(p []byte) (int, error) {
	f.mu.Lock()
	if len(f.pending) == 0 && !f.closed {
		f.mu.Unlock()
		<-f.ready
		f.mu.Lock()
	}
	if f.closed && len(f.pending) == 0 {
		f.mu.Unlock()
		return 0, io.EOF
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	f.mu.Unlock()
	return n, nil
}

func (f *fakePipe) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	select {
	case f.ready <- struct{}{}:
	default:
	}
	return nil
}

// push 注入一段 stdout 数据，唤醒读取协程。
func (f *fakePipe) push(b string) {
	f.mu.Lock()
	f.pending = append(f.pending, []byte(b)...)
	f.mu.Unlock()
	select {
	case f.ready <- struct{}{}:
	default:
	}
}

func (f *fakePipe) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	rest := string(f.written)
	for {
		i := indexByte(rest, '\n')
		if i < 0 {
			break
		}
		out = append(out, rest[:i])
		rest = rest[i+1:]
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// newPair 构造一对假管道并启动客户端读写协程。
func newPair(t *testing.T, onEvent func(json.RawMessage)) (*Client, *fakePipe, *fakePipe) {
	t.Helper()
	in := newFakePipe()
	out := newFakePipe()
	c := New(in, out, 1<<20, onEvent)
	c.Start()
	t.Cleanup(c.Close)
	return c, in, out
}

// waitFor 轮询直到条件满足，避免测试用固定 sleep 造成偶发失败。

func TestCall按ID关联响应(t *testing.T) {
	var seen []string
	c, _, out := newPair(t, func(raw json.RawMessage) { seen = append(seen, string(raw)) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		testutil.WaitFor(t, "命令写入 stdin", func() bool { return c.pendingCount() == 1 })
		out.push(`{"type":"response","id":"rpc-1","success":true,"data":{"ok":1}}` + "\n")
	}()
	raw, err := c.Call(ctx, "get_state", nil)
	if err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	if string(raw) != `{"ok":1}` {
		t.Fatalf("响应数据不匹配: %s", raw)
	}
	if len(seen) != 0 {
		t.Fatalf("响应不应被当作事件转发: %v", seen)
	}
}

func Test事件不按响应处理(t *testing.T) {
	got := make(chan json.RawMessage, 4)
	c, _, out := newPair(t, func(raw json.RawMessage) { got <- raw })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		testutil.WaitFor(t, "命令写入 stdin", func() bool { return c.pendingCount() == 1 })
		out.push(`{"type":"agent_start"}` + "\n")
		out.push(`{"type":"response","id":"rpc-1","success":true,"data":null}` + "\n")
	}()
	if _, err := c.Call(ctx, "get_state", nil); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	select {
	case raw := <-got:
		if string(raw) != `{"type":"agent_start"}` {
			t.Fatalf("事件内容不匹配: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("事件未被转发")
	}
}

func TestCall返回Pi明确错误(t *testing.T) {
	c, _, out := newPair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		testutil.WaitFor(t, "命令写入 stdin", func() bool { return c.pendingCount() == 1 })
		out.push(`{"type":"response","id":"rpc-1","success":false,"error":"模型不存在"}` + "\n")
	}()
	_, err := c.Call(ctx, "set_model", map[string]any{"provider": "x"})
	var re *RPCError
	if !errors.As(err, &re) || re.Message != "模型不存在" {
		t.Fatalf("应返回 RPCError，实际 %v", err)
	}
}

func TestCall连接关闭时结果未知(t *testing.T) {
	c, _, _ := newPair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		testutil.WaitFor(t, "命令写入 stdin", func() bool { return c.pendingCount() == 1 })
		c.fail(io.ErrUnexpectedEOF)
	}()
	_, err := c.Call(ctx, "prompt", map[string]any{"message": "hi"})
	var unknown *UnknownOutcome
	if !errors.As(err, &unknown) {
		t.Fatalf("连接失败应判为结果未知，实际 %v", err)
	}
}

func TestNotify在背压下不挂起调用方(t *testing.T) {
	// 控制帧队列有界（256）。写盘被卡住时，前 256 条入队，
	// 之后的调用必须立即返回 ErrLimit 而不是无限阻塞。
	c, in, _ := newPair(t, nil)
	in.blockWrites()
	t.Cleanup(in.releaseWrites)
	returned := make(chan struct{})
	var limitErrs atomic.Int64
	go func() {
		defer close(returned)
		for i := 0; i < 400; i++ {
			if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "x", "cancelled": true}); err != nil {
				limitErrs.Add(1)
			}
		}
	}()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("Notify 在写入背压下仍返回，不能挂起调用方")
	}
	if limitErrs.Load() == 0 {
		t.Fatal("队列满时应返回错误，让调用方知道没送达")
	}
	// 关键：控制帧队列溢出不得杀死连接，会话还要继续。
	select {
	case <-c.Done():
		t.Fatal("控制帧队列溢出不应关闭连接")
	case <-time.After(200 * time.Millisecond):
	}
}

func Test并发调用写入不交错(t *testing.T) {
	c, in, out := newPair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const total = 8
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		go func() { _, err := c.Call(ctx, "get_state", nil); errs <- err }()
	}
	testutil.WaitFor(t, "所有命令写入 stdin", func() bool { return len(in.commands()) == total })
	for _, line := range in.commands() {
		if len(line) == 0 || line[len(line)-1] != '}' {
			t.Fatalf("写入内容不是完整的一帧: %q", line)
		}
	}
	// 一次性回 8 帧响应，各 Call 按自己的 id 取回结果。
	var replay strings.Builder
	for i := 1; i <= total; i++ {
		replay.WriteString(`{"type":"response","id":"rpc-`)
		replay.WriteString(strconv.Itoa(i))
		replay.WriteString(`","success":true,"data":{}}`)
		replay.WriteString("\n")
	}
	out.push(replay.String())
	for i := 0; i < total; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("并发调用失败: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("并发调用未全部返回")
		}
	}
}

func Test上下文已取消时不写入(t *testing.T) {
	c, in, _ := newPair(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Call(ctx, "get_state", nil); err == nil {
		t.Fatal("已取消的上下文应直接失败")
	}
	time.Sleep(20 * time.Millisecond)
	if len(in.commands()) != 0 {
		t.Fatalf("已取消的调用不应写入 stdin: %v", in.commands())
	}
}

func Test超大帧被拒绝(t *testing.T) {
	c, in, _ := newPair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.Call(ctx, "prompt", map[string]any{"message": string(make([]byte, 2<<20))})
	if !errors.Is(err, jsonl.ErrTooLarge) {
		t.Fatalf("应拒绝超大帧，实际 %v", err)
	}
	testutil.WaitFor(t, "未写入超大帧", func() bool { return len(in.commands()) == 0 })
}

// pendingCount 仅用于测试观察在途命令数。
func (c *Client) pendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func TestNotify在写缓冲满时不杀死连接(t *testing.T) {
	// Notify 用于取消待回复对话，必须送达。曾经的 default 分支会在
	// 写缓冲满（64）时调 fail() 关闭 stdin/stdout，把整个 worker 连接杀掉。
	fp := newFakePipe()
	fp.blockWrites() // 写协程取走后也卡在真正写盘上
	c := New(fp, fp, 1<<20, nil)
	defer c.Close()

	// 控制帧队列容量 256，填满它。
	for i := 0; i < 256; i++ {
		if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": fmt.Sprintf("d-%d", i)}); err != nil {
			t.Fatalf("第 %d 条 Notify 应成功入队: %v", i, err)
		}
	}
	// 连接必须仍然存活：pending 未被清空、done 未关闭。
	if c.pendingCount() != 0 {
		t.Fatal("Notify 不应占用 pending")
	}
	select {
	case <-c.done:
		t.Fatal("写缓冲满不应关闭连接")
	default:
	}
	// 溢出时返回明确错误，但不关闭连接。
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "overflow"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("队列满应返回 ErrLimit，实际 %v", err)
	}
	select {
	case <-c.Done():
		t.Fatal("控制帧队列溢出不得关闭连接")
	default:
	}
	// 关闭后必须返回 ErrClosed，不能无限阻塞。
	c.Close()
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "after-close"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后应返回 ErrClosed，实际 %v", err)
	}
}

func Test控制帧队列独立于命令队列(t *testing.T) {
	// 命令写缓冲 64、控制帧缓冲 256，两者必须独立。
	// 命令缓冲满时控制帧仍要能入队——否则卡住的长命令会让
	// 取消对话、abort 全部失效，Pi 侧继续等待人工输入。
	c, in, _ := newPair(t, nil)
	in.blockWrites()
	t.Cleanup(in.releaseWrites)

	// 用一条已入队的命令占住写协程，再把命令缓冲填满。
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go func() { _, _ = c.Call(ctx, "get_state", nil) }()
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 64; i++ {
		go func() { _, _ = c.Call(ctx, "get_state", nil) }()
	}
	time.Sleep(100 * time.Millisecond)

	// 命令通道已满，控制帧必须仍可入队。
	for i := 0; i < 256; i++ {
		if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": fmt.Sprintf("d-%d", i), "cancelled": true}); err != nil {
			t.Fatalf("第 %d 条控制帧应入队: %v", i, err)
		}
	}
	// 超过控制帧容量才报错，且不杀连接。
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "overflow"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("超出容量应返回 ErrLimit，实际 %v", err)
	}
	select {
	case <-c.Done():
		t.Fatal("队列满不得关闭连接")
	default:
	}
}

// Test超限帧不杀死连接 覆盖 B06：
// Pi 回复超过 maxFrame 时，旧实现直接 fail 并关闭 stdin/stdout，
// 同一个 worker 上后续所有命令都变成 worker_exited。
// 这里验证：超限只让那一条命令失败，连接与后续命令不受影响。
func Test超限帧不杀死连接(t *testing.T) {
	big := strings.Repeat("x", 4096)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := New(inW, outR, 1024, nil)
	c.Start()
	defer c.Close()

	// 回环 fake：读到一条命令，按 id 回一条响应。第一条超限，第二条正常。
	go func() {
		r := bufio.NewReader(inR)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			var cmd struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(line), &cmd) != nil {
				continue
			}
			body := `{"type":"response","id":"` + cmd.ID + `","success":true,"data":{"ok":true}}`
			if cmd.ID == "rpc-1" {
				body = `{"type":"response","id":"rpc-1","success":true,"data":{"blob":"` + big + `"}}`
			}
			if _, err := io.WriteString(outW, body+"\n"); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 第一条：超限，必须明确失败。
	_, err := c.Call(ctx, "get_state", map[string]any{})
	if err == nil {
		t.Fatal("超限帧应让该命令失败")
	}
	if strings.Contains(err.Error(), "未知") {
		t.Fatalf("超限应给出明确错误而不是 unknown: %v", err)
	}
	// 连接必须还活着，第二条命令应正常返回。
	data, err := c.Call(ctx, "get_state", map[string]any{})
	if err != nil {
		t.Fatalf("超限帧之后的命令应成功: %v", err)
	}
	if !strings.Contains(string(data), "ok") {
		t.Fatalf("第二条响应内容异常: %s", data)
	}
	select {
	case <-c.Done():
		t.Fatal("超限帧把连接关掉了")
	default:
	}
}

// TestCall忽略fields里的id与type：顶层 id 由 Call 生成（rpc-N）并用于响应配对，
// type 是命令名——调用方都不能覆盖。
//
// 这不是纯防御性断言：runtime/bash.go 曾经用 fields["id"] 传请求标识，想让它
// 出现在 bash_execution_update 事件上做关联。正因为这里过滤，那个参数从未
// 到达过 Pi，而没有任何测试能发现（当时没有地方观察真实载荷）。
//
// 过滤本身是必须的：桥要靠顶层 id 把响应配回来，被调用方覆盖就会串线。
// 这条测试把「调用方无法影响顶层 id」钉死，免得有人「顺手」放开过滤。
func TestCall忽略fields里的id与type(t *testing.T) {
	c, in, out := newPair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		testutil.WaitFor(t, "命令写入 stdin", func() bool { return c.pendingCount() == 1 })
		out.push(`{"type":"response","id":"rpc-1","success":true,"data":null}` + "\n")
	}()
	if _, err := c.Call(ctx, "bash", map[string]any{
		"id":                 "客户端想覆盖的 id",
		"type":               "客户端想覆盖的 type",
		"command":            "echo hi",
		"excludeFromContext": false,
	}); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	cmds := in.commands()
	if len(cmds) != 1 {
		t.Fatalf("应写出一条命令，得到 %d 条", len(cmds))
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(cmds[0]), &sent); err != nil {
		t.Fatalf("命令不是合法 JSON: %s", cmds[0])
	}
	if sent["id"] != "rpc-1" {
		t.Fatalf("顶层 id 必须是桥生成的配对 id，得到 %v", sent["id"])
	}
	if sent["type"] != "bash" {
		t.Fatalf("方法名不能被 fields 覆盖，得到 %v", sent["type"])
	}
	if sent["command"] != "echo hi" {
		t.Fatalf("普通字段必须原样传递，得到 %v", sent["command"])
	}
}
