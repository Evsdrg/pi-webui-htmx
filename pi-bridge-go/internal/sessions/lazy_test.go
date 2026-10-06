package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeLazySession(t *testing.T, rows []map[string]any) (*Store, string) {
	t.Helper()
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := "lazy-demo"
	path := filepath.Join(sessionDir, id+".jsonl")
	header := map[string]any{"type": "session", "version": 3, "id": id, "timestamp": "2026-09-27T00:00:00Z", "cwd": cwd}
	all := append([]map[string]any{header}, rows...)
	buf := make([]byte, 0, 4096)
	for _, r := range all {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func Test块扫描只认配对角色(t *testing.T) {
	cases := map[string]struct {
		role    string
		content []map[string]any
		want    int
	}{
		"assistant 的 thinking":     {"assistant", []map[string]any{{"type": "thinking", "thinking": "想"}, {"type": "text", "text": "答"}}, 1},
		"assistant 的 image 不算":     {"assistant", []map[string]any{{"type": "image", "data": "x"}}, 0},
		"toolResult 的 image":       {"toolResult", []map[string]any{{"type": "image", "data": "x"}, {"type": "text", "text": "出"}}, 1},
		"toolResult 的 thinking 不算": {"toolResult", []map[string]any{{"type": "thinking", "thinking": "x"}}, 0},
		"user 的 image":             {"user", []map[string]any{{"type": "thinking"}, {"type": "image"}}, 1},
		"user 的 thinking 不算":       {"user", []map[string]any{{"type": "thinking"}}, 0},
	}
	for name, c := range cases {
		raw, err := json.Marshal(c.content)
		if err != nil {
			t.Fatal(err)
		}
		_, got := projectContent(c.role, raw)
		if len(got) != c.want {
			t.Errorf("%s: 期望 %d 个惰性块，实际 %d (%v)", name, c.want, len(got), got)
		}
	}
	// content 是字符串（纯文本消息）时必须返回 nil，不能 panic。
	if _, got := projectContent("assistant", json.RawMessage(`"纯文本"`)); got != nil {
		t.Errorf("字符串 content 应无惰性块: %v", got)
	}
}

func TestThinking读取与校验(t *testing.T) {
	s, id := writeLazySession(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": "问题"}},
		{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "这是思考内容"},
			{"type": "text", "text": "回答"},
		}}},
	})
	got, err := s.Thinking(context.Background(), id, "a1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "这是思考内容" {
		t.Fatalf("思考内容不符: %q", got)
	}
	// 指到 text 块、指到 user 消息、条目不存在，都必须明确报错。
	if _, err := s.Thinking(context.Background(), id, "a1", 1); err == nil {
		t.Error("指到 text 块应报错")
	}
	if _, err := s.Thinking(context.Background(), id, "u1", 0); err == nil {
		t.Error("user 消息应报错")
	}
	if _, err := s.Thinking(context.Background(), id, "nope", 0); err == nil {
		t.Error("不存在的条目应报错")
	}
	if _, err := s.Thinking(context.Background(), id, "a1", -1); err == nil {
		t.Error("负 blockIndex 应报错")
	}
}

// 思考块超过上限时按 rune 边界截断，不能切出非法 UTF-8。
func TestThinking超限按rune边界截断(t *testing.T) {
	// 每个「中」是 3 字节；造出略超 MaxThinkingChars 的内容，且让边界落在
	// 某个多字节字符中间（MaxThinkingChars 通常不是 3 的倍数）。
	repeat := MaxThinkingChars/3 + 5
	s, id := writeLazySession(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": "问"}},
		{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": strings.Repeat("中", repeat)},
		}}},
	})
	got, err := s.Thinking(context.Background(), id, "a1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("思考截断产生了非法 UTF-8，结尾字节 %x", got[len(got)-3:])
	}
	if len(got) > MaxThinkingChars {
		t.Fatalf("截断后仍超过上限: %d", len(got))
	}
}

func TestToolImage读取两种形状并拒绝不支持格式(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n-fake"))
	s, id := writeLazySession(t, []map[string]any{
		{"type": "message", "id": "t1", "parentId": nil, "message": map[string]any{"role": "toolResult", "content": []map[string]any{
			{"type": "image", "data": png, "mimeType": "image/png"},
		}}},
		{"type": "message", "id": "t2", "parentId": "t1", "message": map[string]any{"role": "toolResult", "content": []map[string]any{
			{"type": "image", "source": map[string]any{"type": "base64", "data": png, "media_type": "image/jpeg"}},
		}}},
		{"type": "message", "id": "t3", "parentId": "t2", "message": map[string]any{"role": "toolResult", "content": []map[string]any{
			{"type": "image", "data": png, "mimeType": "image/svg+xml"},
		}}},
	})
	body, mime, err := s.ToolImage(context.Background(), id, "t1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || string(body) != "\x89PNG\r\n\x1a\n-fake" {
		t.Fatalf("平铺形状读取异常: %s %q", mime, body)
	}
	// Anthropic 的 source 嵌套形状。
	_, mime2, err := s.ToolImage(context.Background(), id, "t2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if mime2 != "image/jpeg" {
		t.Fatalf("嵌套形状应取到 media_type: %s", mime2)
	}
	// SVG 是可执行内容，必须拒绝。
	if _, _, err := s.ToolImage(context.Background(), id, "t3", 0); err == nil {
		t.Error("SVG 应被拒绝")
	}
	// 非 toolResult 消息必须拒绝。
	if _, _, err := s.ToolImage(context.Background(), id, "nope", 0); err == nil {
		t.Error("不存在的条目应报错")
	}
}

// Test大条目跨缓冲区仍能读到 是回归测试。
//
// rawEntry 曾经手写 chunk 扫描：消耗会话头后 offset 被推进两次
// （先 +i+1，再 +整个 n），于是跨缓冲区的行既被跳过又被截断。
// 小文件碰不到，真实会话里稍大的条目就整条找不到——占位符渲染得出来，
// 点开却永远报「条目不存在」。
func Test大条目跨缓冲区仍能读到(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := "lazy-big"
	header := map[string]any{"type": "session", "version": 3, "id": id, "timestamp": "2026-09-27T00:00:00Z", "cwd": cwd}
	lines := []string{}
	hb, _ := json.Marshal(header)
	lines = append(lines, string(hb))
	// parent 用真正的 JSON null 起始：写成字符串 "null" 是坏数据，
	// 扫描索引会（正确地）按父链断裂拒绝。
	var parent any
	// 每条 40 KB 以上的思考，远超 32 KB 读缓冲，且数量足够跨多个缓冲区。
	for i := 0; i < 12; i++ {
		eid := "big" + string(rune('a'+i))
		row := map[string]any{"type": "message", "id": eid, "parentId": parent, "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": strings.Repeat("思", 20000)},
			{"type": "text", "text": "答"},
		}}}
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
		parent = eid
	}
	if err := os.WriteFile(filepath.Join(sessionDir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// 最后一条：跨越了多个缓冲区，旧实现必然找不到。
	got, err := store.Thinking(context.Background(), id, "bigl", 0)
	if err != nil {
		t.Fatalf("跨缓冲区的条目读不到: %v", err)
	}
	if len(got) != 60000 { // "思" 为 3 字节
		t.Fatalf("思考内容长度异常: %d", len(got))
	}
}

// B38/O03：惰性读取复用 History 的扫描索引。
// 老实现每次从文件头线性扫到目标条目，展开多个旧思考块会把长会话反复扫很多遍。
func Test惰性读取复用扫描索引(t *testing.T) {
	store, id := writeLazySession(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": "问题"}},
		{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "第一段思考"},
			{"type": "text", "text": "回答一"},
		}}},
		{"type": "message", "id": "a2", "parentId": "a1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "第二段思考"},
			{"type": "text", "text": "回答二"},
		}}},
	})
	ctx := context.Background()
	// 冷路径：第一次读取做一次完整扫描，并写入扫描缓存。
	if got, err := store.Thinking(ctx, id, "a1", 0); err != nil || got != "第一段思考" {
		t.Fatalf("冷路径读取失败: %q %v", got, err)
	}
	nodes, _ := store.scan.stats()
	if nodes == 0 {
		t.Fatal("惰性读取应复用扫描索引（旧实现完全不碰缓存）")
	}
	// 热路径：另一条从缓存按偏移直接读，内容仍必须正确。
	if got, err := store.Thinking(ctx, id, "a2", 0); err != nil || got != "第二段思考" {
		t.Fatalf("热路径读取失败: %q %v", got, err)
	}
	if _, err := store.Thinking(ctx, id, "nope", 0); err == nil {
		t.Fatal("不存在的条目仍应报错")
	}
}

// writeLazySessionWithFiller 造一个超出尾部窗口的会话：
// 目标条目在最前，后面接若干条大填充记录（每条约 768 KiB），
// 使文件远离「尾部 4 MiB 窗口」——目标只有在完整索引里才能按偏移直读。
func writeLazySessionWithFiller(t *testing.T, prefix []map[string]any) (*Store, string, string) {
	t.Helper()
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := "lazy-window"
	path := filepath.Join(sessionDir, id+".jsonl")
	header := map[string]any{"type": "session", "version": 3, "id": id, "timestamp": "2026-09-27T00:00:00Z", "cwd": cwd}
	rows := append([]map[string]any{header}, prefix...)
	filler := strings.Repeat("填", 256*1024)
	parent := any(nil)
	if len(prefix) > 0 {
		parent = prefix[len(prefix)-1]["id"]
	}
	for i := 0; i < 8; i++ {
		parentID := parent
		row := map[string]any{"type": "message", "id": fmt.Sprintf("f%d", i), "parentId": parentID, "message": map[string]any{"role": "toolResult", "content": []map[string]any{{"type": "text", "text": filler}}}}
		rows = append(rows, row)
		parent = fmt.Sprintf("f%d", i)
	}
	buf := make([]byte, 0, 7<<20)
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		t.Fatal(err)
	}
	return store, id, path
}

// O03 的另一半：目标在尾部窗口之外时，第一次展开应全扫一次并把**完整**索引
// 写进缓存——此后任何位置的展开都直接按偏移读取，而不是每次从头线性扫。
// 旧实现在窗口未命中时每次线性扫描且从不更新缓存（长会话每次约 146 ms）。
func Test窗口外旧条目展开建完整索引(t *testing.T) {
	store, id, path := writeLazySessionWithFiller(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": "问题"}},
		{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "旧思考一"},
			{"type": "text", "text": "回答"},
		}}},
		{"type": "message", "id": "a2", "parentId": "a1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "旧思考二"},
			{"type": "text", "text": "回答"},
		}}},
	})
	ctx := context.Background()
	// 目标在窗口之外（文件 ~6 MiB，窗口只有尾部 4 MiB）：读取必须正确。
	if got, err := store.Thinking(ctx, id, "a1", 0); err != nil || got != "旧思考一" {
		t.Fatalf("窗口外条目读取失败: %q %v", got, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, complete, ok := store.scan.get(path, st.Size(), st.ModTime().UnixNano())
	if !ok {
		t.Fatal("第一次展开后应有扫描缓存")
	}
	if !complete {
		t.Fatal("窗口未命中时应收敛到完整索引并缓存；当前缓存仍是不完整的尾部窗口")
	}
	// 完整索引落盘后，另一条旧条目（同样在窗口外）也走索引路径且内容正确。
	if got, err := store.Thinking(ctx, id, "a2", 0); err != nil || got != "旧思考二" {
		t.Fatalf("第二条旧条目读取失败: %q %v", got, err)
	}
	if _, err := store.Thinking(ctx, id, "missing", 0); err == nil {
		t.Fatal("完整索引里不存在的条目仍应报错")
	}
}

// 全扫失败（文件含损坏行）时必须保留旧的宽松线性扫描兜底：
// 损坏位置之前的条目仍能读到，而不是把整份文件判死。
func Test窗口外条目不因中途损坏行而不可读(t *testing.T) {
	store, id, path := writeLazySessionWithFiller(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": "问题"}},
		{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "损坏之前的思考"},
			{"type": "text", "text": "回答"},
		}}},
	})
	// 在文件中部插入一行坏 JSON：完整扫描会拒绝它，线性扫描会跳过它。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mid := len(data) / 2
	for mid < len(data) && data[mid] != '\n' {
		mid++
	}
	broken := append([]byte{}, data[:mid+1]...)
	broken = append(broken, []byte("{oops\n")...)
	broken = append(broken, data[mid+1:]...)
	if err := os.WriteFile(path, broken, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := store.Thinking(context.Background(), id, "a1", 0)
	if err != nil {
		t.Fatalf("完整扫描失败时不应把损坏位置之前的条目判死: %v", err)
	}
	if got != "损坏之前的思考" {
		t.Fatalf("内容不符: %q", got)
	}
}
