package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestScanLazyBlocks只认配对角色(t *testing.T) {
	cases := map[string]struct {
		role    string
		content []map[string]any
		want    int
	}{
		"assistant 的 thinking":     {"assistant", []map[string]any{{"type": "thinking", "thinking": "想"}, {"type": "text", "text": "答"}}, 1},
		"assistant 的 image 不算":     {"assistant", []map[string]any{{"type": "image", "data": "x"}}, 0},
		"toolResult 的 image":       {"toolResult", []map[string]any{{"type": "image", "data": "x"}, {"type": "text", "text": "出"}}, 1},
		"toolResult 的 thinking 不算": {"toolResult", []map[string]any{{"type": "thinking", "thinking": "x"}}, 0},
		"user 什么都不算":               {"user", []map[string]any{{"type": "thinking"}, {"type": "image"}}, 0},
	}
	for name, c := range cases {
		raw, err := json.Marshal(map[string]any{"role": c.role, "content": c.content})
		if err != nil {
			t.Fatal(err)
		}
		got := scanLazyBlocks(raw)
		if len(got) != c.want {
			t.Errorf("%s: 期望 %d 个惰性块，实际 %d (%v)", name, c.want, len(got), got)
		}
	}
	// content 是字符串（纯文本消息）时必须返回 nil，不能 panic。
	str, _ := json.Marshal(map[string]any{"role": "assistant", "content": "纯文本"})
	if got := scanLazyBlocks(str); got != nil {
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
	parent := "null"
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
