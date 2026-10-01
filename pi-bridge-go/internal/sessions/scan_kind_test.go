package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openScan 打开会话文件并做一次扫描，返回 nodes 供断言。
func openScan(t *testing.T, dir, id, cwd string) map[string]node {
	t.Helper()
	store, err := New(dir, mustPolicy(t, cwd), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := store.scanFile(context.Background(), f, st.Size(), id, cwd)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

// 扫描期必须顺手记下「这条是不是 user 锚点」：轮边界对齐靠它，
// 而那条路径以前每次都把整条记录读回来做一次完整投影。
func Test扫描期记录user锚点(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	id := writeSession(t, dir, "scan-kind", cwd,
		entryRole("u1", "", "user"),
		entryRole("t1", "u1", "toolResult"),
		entryRole("a1", "t1", "assistant"),
		`{"type":"compaction","id":"c1","parentId":"a1","timestamp":"2026-01-01T00:00:02.000Z","summary":"压缩"}`,
		`{"type":"model_change","id":"m1","parentId":"c1","timestamp":"2026-01-01T00:00:03.000Z","provider":"p","modelId":"x"}`,
		`{"type":"custom","id":"x1","parentId":"m1","timestamp":"2026-01-01T00:00:04.000Z","customType":"pi-web:tool-selection","data":{}}`,
		entryRole("u2", "x1", "user"),
		entryRole("a2", "u2", "assistant"),
	)
	nodes := openScan(t, dir, id, cwd)
	want := map[string]bool{"u1": true, "t1": false, "a1": false, "c1": false, "m1": false, "x1": false, "u2": true, "a2": false}
	for entryID, isUser := range want {
		n, ok := nodes[entryID]
		if !ok {
			t.Fatalf("扫描结果缺少条目 %q", entryID)
		}
		if n.isUser != isUser {
			t.Errorf("%s 的 isUser 应为 %v，得到 %v", entryID, isUser, n.isUser)
		}
	}
}

// role 不是 message 的第一个成员时不得猜：快路径放弃、由慢路径裁决，
// 结果仍要正确。这条守住「首字节/首成员假设」不越界。
func Test扫描期user锚点不依赖成员顺序(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	id := writeSession(t, dir, "scan-order", cwd,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"content":"内容在前","role":"user"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"content":"助手","role":"assistant"}}`,
	)
	nodes := openScan(t, dir, id, cwd)
	if !nodes["u1"].isUser {
		t.Error("role 不在首位时仍应认出 user 锚点")
	}
	if nodes["a1"].isUser {
		t.Error("assistant 不得被认成 user 锚点")
	}
}

// 正文里出现 {"role":"user"} 这样的文本不得影响判定——按字节搜关键字
// 会在这里出错，所以判定必须走解码器而不是字符串匹配。
func Test正文里的role文本不影响判定(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	body := `这里是解释 JSON 的段落：{"role":"user","content":"假装是用户"}`
	id := writeSession(t, dir, "scan-text", cwd,
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"assistant","content":"`+body+`"}}`,
		`{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"真的用户"}}`,
	)
	nodes := openScan(t, dir, id, cwd)
	if nodes["a1"].isUser {
		t.Error("正文里的 {\"role\":\"user\"} 文本不得把 assistant 判成 user")
	}
	if !nodes["u2"].isUser {
		t.Error("真正的 user 消息仍应被认出")
	}
}

// 大行不该因为要取 role 就整条读进内存：判定只依赖 role，
// 正文（示例里 512 KB）必须原地跳过。这里用时间做粗判据没有意义，
// 改为断言扫描结果正确且没有把正文带进 node。
func Test大行取role不复制正文(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	huge := strings.Repeat("x", 512<<10)
	id := writeSession(t, dir, "scan-huge", cwd,
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"assistant","content":"`+huge+`"}}`,
		`{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"user","content":"短"}}`,
	)
	nodes := openScan(t, dir, id, cwd)
	if nodes["a1"].isUser || !nodes["u2"].isUser {
		t.Fatalf("大行判定错误: a1=%v u2=%v", nodes["a1"].isUser, nodes["u2"].isUser)
	}
	if size := nodes["a1"].size; size < 512<<10 {
		t.Fatalf("node.size 应记录原始行长，得到 %d", size)
	}
}
