package presentation

import (
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
)

// 汇总必须累加一个回合里的多条 assistant 记录：带工具调用的回合
// 会有多条，只取最后一条会少算。
func Test汇总整个回合的用量(t *testing.T) {
	entries := []sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "hi"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "let me look", Usage: &sessions.Usage{Input: 100, Output: 10, CacheRead: 50, Cost: 0.001}},
		{ID: "t1", Kind: sessions.KindTool, Text: "ok"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "done", Usage: &sessions.Usage{Input: 200, Output: 20, CacheRead: 60, Cost: 0.002}},
	}
	turns := GroupTurns(entries)
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合，得到 %d", len(turns))
	}
	u := turns[0].Usage
	if u == nil {
		t.Fatal("回合应带用量")
	}
	if u.Input != 300 || u.Output != 30 || u.CacheRead != 110 {
		t.Fatalf("应累加：%+v", u)
	}
	if u.Cost < 0.0029 || u.Cost > 0.0031 {
		t.Fatalf("费用应累加：%v", u.Cost)
	}
	if !strings.Contains(u.Summary(), "300 in") {
		t.Fatalf("摘要应体现累加值：%q", u.Summary())
	}
}
