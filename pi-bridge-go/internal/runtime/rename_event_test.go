package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 桥消费 Pi 的 session_info_changed 事件来失效会话索引。
//
// 为什么需要它：Pi 改名只是往会话文件尾部追加一条 session_info，而文件位于
// cwd 编码的子目录里——这种追加既不改变顶层文件数、也不改变任何顶层条目的
// mtime，索引的顶层戳判定看不见它，会继续复用缓存的旧标题。事件是唯一可靠的
// 信号（rpc-mode 把 session 事件透传到 stdout，见 rpc-mode.js 的 session.subscribe）。
func Test改名事件让会话索引失效(t *testing.T) {
	m, cwd := newTestManager(t)
	store := m.cfg.Store

	// 造一个嵌套布局的会话文件，标题回退到首条用户消息。
	encoded := "--" + filepath.ToSlash(filepath.Clean(cwd)) + "--"
	encoded = replaceDash(encoded)
	dir := filepath.Join(store.Dir(), encoded)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	id := "renamed1"
	path := filepath.Join(dir, "2026-01-01T00-00-00.000Z_"+id+".jsonl")
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"原标题"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	name := func() string {
		listing, err := store.List(context.Background(), 0, 10, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(listing.Items) != 1 {
			t.Fatalf("应列出 1 条会话，实际 %d", len(listing.Items))
		}
		return listing.Items[0].Name
	}
	if got := name(); got != "原标题" {
		t.Fatalf("首次标题应为「原标题」，实际 %q", got)
	}

	// 模拟 Pi 改名：追加 session_info。仅追加不足以让索引发现（子目录布局）。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"session_info","id":"n1","name":"新标题"}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	// 桥收到事件后应失效索引。用真实 worker（经 m.Start 构造，字段完整）。
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.event([]byte(`{"type":"session_info_changed","name":"新标题"}`))

	if got := name(); got != "新标题" {
		t.Fatalf("改名事件后应读到新标题，实际 %q（事件未驱动索引失效）", got)
	}
}

// 与改名无关的事件不该白跑一次索引失效（否则每次事件都触发全量重建）。
// 用 Index 的调试快照里的 builtAt 判定，不依赖未导出字段。
func Test无关事件不失效索引(t *testing.T) {
	m, cwd := newTestManager(t)
	store := m.cfg.Store

	// 先让索引建立，取一份带 builtAt 的快照。
	if _, err := store.List(context.Background(), 0, 10, ""); err != nil {
		t.Fatal(err)
	}
	before := store.Index().Stats()["builtAt"]

	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	w.event([]byte(`{"type":"agent_start"}`))
	w.event([]byte(`{"type":"queue_update","steering":[],"followUp":[]}`))

	if after := store.Index().Stats()["builtAt"]; after != before {
		t.Fatalf("无关事件不应失效索引：%v → %v", before, after)
	}
}

// replaceDash 把 cwd 编码成 Pi 的子目录名（/ 换 -）。
func replaceDash(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '/' {
			out = append(out, '-')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
