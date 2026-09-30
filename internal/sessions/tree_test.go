package sessions

import (
	"context"
	"testing"
)

// U04：磁盘树投影。浏览历史分支不该拉起 Pi 进程，
// 所以没有 worker 时也要能给出树结构。
func Test磁盘树投影出分支与叶子(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	// u1 → a1 → {a2, b2}：a2 是当前叶子，b2 是另一条分支。
	writeSession(t, dir, "branched", cwd,
		entry("u1", ""),
		entryRole("a1", "u1", "assistant"),
		entryRole("a2", "a1", "assistant"),
		entryRole("b2", "a1", "assistant"))
	tree, err := store.Tree(context.Background(), "branched")
	if err != nil {
		t.Fatal(err)
	}
	roots, _ := tree["tree"].([]any)
	if len(roots) != 1 {
		t.Fatalf("应只有一个根: %+v", tree)
	}
	root := roots[0].(map[string]any)
	entryOf := func(n map[string]any) string {
		e, _ := n["entry"].(map[string]any)
		id, _ := e["id"].(string)
		return id
	}
	if entryOf(root) != "u1" {
		t.Fatalf("根应是 u1: %+v", root)
	}
	// 逐层下钻：u1 → a1 → 两个子节点。
	level1, _ := root["children"].([]any)
	if len(level1) != 1 || entryOf(level1[0].(map[string]any)) != "a1" {
		t.Fatalf("u1 下应是 a1: %+v", level1)
	}
	level2, _ := level1[0].(map[string]any)["children"].([]any)
	if len(level2) != 2 {
		t.Fatalf("a1 下应是两个分支: %+v", level2)
	}
	if entryOf(level2[0].(map[string]any)) != "a2" || entryOf(level2[1].(map[string]any)) != "b2" {
		t.Fatalf("分支顺序应为文件顺序: %+v", level2)
	}
	// leafId 是磁盘上的最后一个条目。
	if leaf, _ := tree["leafId"].(string); leaf != "b2" {
		t.Fatalf("leafId 应是最后落盘的条目: %q", leaf)
	}
	// entry 带摘要（角色或文本），否则界面上全是裸 ID。
	first := level2[0].(map[string]any)["entry"].(map[string]any)
	if first["type"] == nil {
		t.Fatalf("entry 应带 type: %+v", first)
	}
	msg, _ := first["message"].(map[string]any)
	if msg["role"] != "assistant" {
		t.Fatalf("entry 应带角色: %+v", first)
	}
}
