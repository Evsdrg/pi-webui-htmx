package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/protocol"
	"pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
)

// Test每个受支持方法都有执行策略 是跨包静态核对：
// specs 在 protocol，注册表在 transport，两者必须一一对应。
// 漏列的方法走不到 intent 与控制队列，跨重启去重和取消都会被绕过。
func Test每个受支持方法都有执行策略(t *testing.T) {
	registered := map[string]bool{}
	for _, m := range SupportedMethods {
		registered[m] = true
		if _, ok := protocol.SpecFor(m); !ok {
			t.Errorf("方法 %q 缺少执行策略", m)
		}
	}
	if got := protocol.SpecCount(); got != len(SupportedMethods) {
		t.Fatalf("策略数 %d 与方法数 %d 不一致", got, len(SupportedMethods))
	}
	for _, m := range SupportedMethods {
		if !registered[m] {
			t.Errorf("注册表出现重复方法 %q", m)
		}
	}
}

// Test清单文档与实现一致 防止 docs/method-inventory.md 与代码漂移。
func Test清单文档与实现一致(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "method-inventory.md"))
	if err != nil {
		t.Skip("跳过：方法清单文档缺失（docs/method-inventory.md）")
	}
	for _, m := range SupportedMethods {
		if !strings.Contains(string(doc), "`"+m+"`") {
			t.Errorf("清单文档缺少方法 %q 的分类说明", m)
		}
	}
}

// Test并发claim只有一个放行 覆盖 B04 的核心：
// 以前去重状态只在单连接内，两个连接可以同时通过检查并各自执行
// 同一个有副作用的 requestId。这里用并发压测验证只有一个放行。
func Test并发claim只有一个放行(t *testing.T) {
	c := newClaims(1024)
	const n = 64
	var proceed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if state, _ := c.begin("same-id", "fp", nil); state == claimProceed {
				atomic.AddInt32(&proceed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(&proceed); got != 1 {
		t.Fatalf("并发同一 requestId 应只有一个放行，实际 %d", got)
	}
	stats := c.stats()
	if stats["pending"].(int) != 1 {
		t.Fatalf("应只有一个在途登记: %v", stats)
	}
}

// Testclaim淘汰不动在途登记 覆盖内存边界：
// 淘汰已完成的旧登记可以，淘汰在途登记会让重试变成第二次执行。
func TestClaim淘汰不动在途登记(t *testing.T) {
	c := newClaims(4)
	for i := 0; i < 8; i++ {
		id := "done-" + strconv.Itoa(i)
		if state, _ := c.begin(id, "fp", nil); state != claimProceed {
			t.Fatalf("%s 应放行", id)
		}
		c.finish(id)
	}
	// 一个在途登记 + 多个已完成登记，持续挤压上限。
	if state, _ := c.begin("in-flight", "fp", nil); state != claimProceed {
		t.Fatal("在途登记应放行")
	}
	for i := 0; i < 20; i++ {
		id := "later-" + strconv.Itoa(i)
		c.begin(id, "fp", nil)
		c.finish(id)
	}
	if _, ok := c.byID["in-flight"]; !ok {
		t.Fatal("在途登记被淘汰了")
	}
	stats := c.stats()
	if stats["tracked"].(int) > 4 {
		t.Fatalf("登记数超过上限: %v", stats)
	}
}

// Test同一requestId换内容报conflict 覆盖指纹校验：
// 复用 requestId 发不同命令是客户端错误，不能回放另一条命令的结论。
func Test同一requestId换内容报conflict(t *testing.T) {
	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		header := http.Header{}
		header.Set("Authorization", "Bearer "+testToken)
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		if err != nil {
			t.Fatalf("连接失败: %v", err)
		}
		return conn
	}
	send := func(conn *websocket.Conn, v map[string]any) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func(conn *websocket.Conn) map[string]any {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	conn := dial()
	defer conn.CloseNow()
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "fp-1", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	if m := read(conn); m["ok"] != true {
		t.Fatalf("启动失败: %v", m)
	}
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "fp-1", "method": "worker.list"})
	if m := read(conn); replyCode(m) != "conflict" {
		t.Fatalf("同一 requestId 换方法应报 conflict: %v", m)
	}
	send(conn, map[string]any{"version": 1, "kind": "command", "requestId": "fp-1", "method": "session.start", "params": map[string]any{"cwd": "/other"}})
	if m := read(conn); replyCode(m) != "conflict" {
		t.Fatalf("同一 requestId 换参数应报 conflict: %v", m)
	}
}

// Test重启后pending回执回答unknown 覆盖 B30：
// intent 已落盘但结论没写时，桥无法证明命令是否到达 Pi，
// 只能回答 unknown，绝不假装成功。
func Test重启后pending回执回答unknown(t *testing.T) {
	dir := t.TempDir()
	req := protocol.Request{
		Version: 1, Kind: "command", RequestID: "crashed-1",
		Method: "session.prompt", Params: []byte(`{}`),
	}
	receipts, err := storage.NewReceipts(dir, storage.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := receipts.Record(storage.Receipt{
		RequestID: "crashed-1", Method: "session.prompt",
		Outcome: storage.OutcomePending, Fingerprint: requestFingerprint(req),
	}); err != nil {
		t.Fatal(err)
	}
	if err := receipts.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.NewReceipts(dir, storage.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rec, ok := reopened.Lookup("crashed-1")
	if !ok || rec.Outcome != storage.OutcomePending {
		t.Fatalf("pending 回执应被保留: %+v %v", rec, ok)
	}
	s := &Server{receipts: reopened, claims: newClaims(16)}
	var got protocol.Message
	ok2, _ := s.admit(req, func(m protocol.Message) { got = m })
	if ok2 {
		t.Fatal("pending 回执不得放行重发")
	}
	body, _ := json.Marshal(got)
	var frame map[string]any
	_ = json.Unmarshal(body, &frame)
	if code := replyCode(frame); code != "outcome_unknown" {
		t.Fatalf("应回答 outcome_unknown，实际 %q", code)
	}
	// 换内容的 pending 回执要先报 conflict，不能拿旧 intent 顶替。
	ok3, _ := s.admit(protocol.Request{
		Version: 1, Kind: "command", RequestID: "crashed-1",
		Method: "session.stop", Params: []byte(`{}`),
	}, func(m protocol.Message) { got = m })
	if ok3 {
		t.Fatal("换内容的 requestId 不得放行")
	}
	body, _ = json.Marshal(got)
	_ = json.Unmarshal(body, &frame)
	if code := replyCode(frame); code != "conflict" {
		t.Fatalf("应报 conflict，实际 %q", code)
	}
}

// blockingSink 是一个在 dispatch 期间可观测回执状态的假连接。
type blockingSink struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	reply   func(protocol.Message)
}

func (b *blockingSink) connContext() context.Context { return context.Background() }
func (b *blockingSink) send(m protocol.Message) bool {
	if b.reply != nil {
		b.reply(m)
	}
	return true
}
func (b *blockingSink) trackTerminal(string, *terminal.Subscription)      {}
func (b *blockingSink) dropTerminal(string)                               {}
func (b *blockingSink) sendRaw(context.Context, []byte) bool              { return true }
func (b *blockingSink) trackSubscription(string, *runtime.Subscription)   {}
func (b *blockingSink) existingSubscription(string) *runtime.Subscription { return nil }
func (b *blockingSink) dispatch(context.Context, protocol.Request) (any, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return map[string]any{"accepted": true}, nil
}

// TestIntent先于派发落盘 覆盖 B30 的时序：
// 有副作用的命令必须在派发前写 intent。只有「派发中已在盘上」才成立；
// 派发后才写的话，桥在这段窗口里崩溃依旧无法判断是否执行过。
func TestIntent先于派发落盘(t *testing.T) {
	s, _, _ := newTestServer(t)
	sink := &blockingSink{release: make(chan struct{}), entered: make(chan struct{})}
	req := protocol.Request{
		Version: 1, Kind: "command", RequestID: "intent-1",
		Method: "session.prompt", Params: []byte(`{"text":"hi"}`),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runCommand(sink, req)
	}()
	<-sink.entered
	// 命令正在执行：此刻 intent 必须已经落盘。
	rec, ok := s.receipts.Lookup("intent-1")
	if !ok {
		t.Fatal("派发期间回执里没有 intent")
	}
	if rec.Outcome != storage.OutcomePending {
		t.Fatalf("派发期间应处于 pending，实际 %q", rec.Outcome)
	}
	if rec.Fingerprint == "" {
		t.Fatal("intent 缺少指纹，重启后无法校验内容")
	}
	close(sink.release)
	<-done
	rec, _ = s.receipts.Lookup("intent-1")
	if rec.Outcome != storage.OutcomeOK {
		t.Fatalf("完成后应落终态，实际 %q", rec.Outcome)
	}
	// claim 必须已释放，否则同一 requestId 的合法重发会被永远挡住。
	if stats := s.claims.stats(); stats["pending"].(int) != 0 {
		t.Fatalf("claim 未释放: %v", stats)
	}
}

// Test只读命令不写intent 覆盖反向边界：
// 给纯读命令写 intent 会白白消耗回执预算与磁盘。
func Test只读命令不写intent(t *testing.T) {
	s, _, _ := newTestServer(t)
	sink := &blockingSink{release: make(chan struct{}), entered: make(chan struct{})}
	close(sink.release)
	s.runCommand(sink, protocol.Request{
		Version: 1, Kind: "command", RequestID: "read-1",
		Method: "worker.list", Params: []byte(`{}`),
	})
	if _, ok := s.receipts.Lookup("read-1"); ok {
		t.Fatal("只读命令不应写回执")
	}
}

// Test响应到达时终态回执已落盘 覆盖回执与响应的先后顺序（真缺陷，不是抖动）。
//
// 客户端收到响应后可能**立刻**用同一 requestId 重发（重试或误重发）。
// admit 对「intent 已写、终态未落」的回答是 outcome_unknown——那是崩溃恢复
// 才该有的结论。因此只要响应已经发出，终态回执就必须已经在盘上；否则一条
// **已经成功**的命令会被判成「结果未知」，客户端据此对账/告警全是错的。
//
// 判据是确定性的：在 send 被调用的那一刻读回执，而不是靠并发撞窗口。
func Test响应到达时终态回执已落盘(t *testing.T) {
	s, _, _ := newTestServer(t)
	var seen bool
	var outcome storage.Outcome
	sink := &blockingSink{
		release: make(chan struct{}),
		entered: make(chan struct{}),
		reply: func(protocol.Message) {
			// 响应发出的这一刻：终态回执必须已经落盘。
			rec, ok := s.receipts.Lookup("order-1")
			seen, outcome = ok, rec.Outcome
		},
	}
	close(sink.release)
	s.runCommand(sink, protocol.Request{
		Version: 1, Kind: "command", RequestID: "order-1",
		Method: "session.prompt", Params: []byte(`{"text":"hi"}`),
	})
	if !seen {
		t.Fatal("响应发出时回执尚未落盘，客户端重发会撞上 outcome_unknown 的窗口")
	}
	if outcome != storage.OutcomeOK {
		t.Fatalf("响应发出时应已是终态 ok，实际 %q", outcome)
	}
}
