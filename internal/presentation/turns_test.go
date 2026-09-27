package presentation

import (
	"testing"

	"pi-bridge-go/internal/sessions"
)

// assistantEntry 造一条带思考块的 assistant 条目。
func assistantEntry(id string, blocks ...int) sessions.Entry {
	lazy := []sessions.LazyBlock{}
	for _, b := range blocks {
		lazy = append(lazy, sessions.LazyBlock{Kind: "thinking", BlockIndex: b})
	}
	return sessions.Entry{ID: id, Kind: sessions.KindAssistant, Text: "回答 " + id, Lazy: lazy}
}

// Test思考占位符归属各自条目 覆盖 B11：
// 一个回合里多个 assistant 条目各带思考块时，旧实现把 AssistantEntryID
// 覆盖成最后一个条目，却把各条目的块下标合并，于是较早的块按错误 ID 去取，
// 既取不回原文，也可能重复出现同一段。
func Test思考占位符归属各自条目(t *testing.T) {
	entries := []sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		assistantEntry("a1", 0),
		assistantEntry("a2", 0, 1),
	}
	turns := GroupTurns(entries)
	if len(turns) != 1 {
		t.Fatalf("应聚合成一个回合: %d", len(turns))
	}
	got := turns[0].Thinking
	if len(got) != 3 {
		t.Fatalf("应有 3 个占位符，实际 %d: %+v", len(got), got)
	}
	want := []ThinkingBlock{
		{EntryID: "a1", BlockIndex: 0},
		{EntryID: "a2", BlockIndex: 0},
		{EntryID: "a2", BlockIndex: 1},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个占位符归属错误: %+v，期望 %+v", i, got[i], want[i])
		}
	}
}

// Test孤儿assistant的思考归属 覆盖无 user 锚点分支：
// 孤儿 assistant 单独成轮，占位符仍须指向它自己。
func Test孤儿assistant的思考归属(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{assistantEntry("solo", 2)})
	if len(turns) != 1 {
		t.Fatalf("应单独成轮: %d", len(turns))
	}
	if len(turns[0].Thinking) != 1 || turns[0].Thinking[0].EntryID != "solo" || turns[0].Thinking[0].BlockIndex != 2 {
		t.Fatalf("孤儿回合占位符归属错误: %+v", turns[0].Thinking)
	}
}

// Test没有思考块时不产生占位符 覆盖反向边界。
func Test没有思考块时不产生占位符(t *testing.T) {
	entries := []sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "回答"},
	}
	turns := GroupTurns(entries)
	if len(turns) != 1 || len(turns[0].Thinking) != 0 {
		t.Fatalf("不应产生占位符: %+v", turns)
	}
}
