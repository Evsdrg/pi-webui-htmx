package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 下面的结构体刻意按 Pi 真实的字段顺序声明：type/id/parentId 在前，
// 大字段 message 在最后。
//
// 这一点直接影响基准的有效性：曾经用 map[string]any 造数据，Go 的 JSON
// 编码会按字母序排键，message 跑到 parentId 前面，早停解析还没来得及停
// 就得先跳过整个大字段——基准因此完全测不到优化的效果，还误判成回退。
// 真实会话的键序见 zz_ab_test.go 对 ~/.pi/agent 的统计。
type benchHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

type benchMessage struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

func benchMsg(role string, content any) json.RawMessage {
	b, err := json.Marshal(map[string]any{"role": role, "content": content})
	if err != nil {
		panic(err)
	}
	return b
}

// buildBigSession 造一个规模接近真实长会话的 JSONL 文件。
//
// 每轮 = user + assistant(带思考) + toolResult + assistant。
// 思考块给 6 KB，让文件体积也接近真实值——只看条目数会低估扫描成本。
func buildBigSession(tb testing.TB, turns int) (*Store, string, string) {
	tb.Helper()
	cwd := tb.TempDir()
	// newStore 收 *testing.T；这里统一转一次，避免调用方各自断言。
	t, _ := tb.(*testing.T)
	if t == nil {
		t = &testing.T{}
	}
	store, sessionDir := newStore(t, cwd)
	id := "bench-big"
	path := filepath.Join(sessionDir, id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(benchHeader{Type: "session", Version: 3, ID: id, Timestamp: "2026-01-01T00:00:00.000Z", Cwd: cwd}); err != nil {
		tb.Fatal(err)
	}
	think := strings.Repeat("思", 2048) // 约 6 KB UTF-8
	toolOut := strings.Repeat("工具输出 ", 40)
	parent := (*string)(nil)
	for i := 0; i < turns; i++ {
		u, tk, r, a := fmt.Sprintf("u%d", i), fmt.Sprintf("t%d", i), fmt.Sprintf("r%d", i), fmt.Sprintf("a%d", i)
		for _, e := range []benchMessage{
			{Type: "message", ID: u, ParentID: parent, Timestamp: "2026-01-01T00:00:01.000Z",
				Message: benchMsg("user", fmt.Sprintf("第 %d 轮：请检查工作区结构并给出建议。", i))},
			{Type: "message", ID: tk, ParentID: &u, Timestamp: "2026-01-01T00:00:02.000Z",
				Message: benchMsg("assistant", []map[string]any{
					{"type": "thinking", "thinking": think},
					{"type": "text", "text": "正在检查"},
				})},
			{Type: "message", ID: r, ParentID: &tk, Timestamp: "2026-01-01T00:00:03.000Z",
				Message: benchMsg("toolResult", []map[string]any{
					{"type": "text", "text": toolOut},
				})},
			{Type: "message", ID: a, ParentID: &r, Timestamp: "2026-01-01T00:00:04.000Z",
				Message: benchMsg("assistant", []map[string]any{
					{"type": "text", "text": fmt.Sprintf("第 %d 轮回答：建议补充测试并复核边界条件。", i)},
				})},
		} {
			if err := enc.Encode(e); err != nil {
				tb.Fatal(err)
			}
		}
		parent = &a
	}
	return store, id, path
}

// BenchmarkHistory首页 是用户打开会话时的路径：一次全文件扫描 + 取末 50 条。
func BenchmarkHistory首页(b *testing.B) {
	for _, turns := range []int{100, 500, 2000} {
		store, id, _ := buildBigSession(b, turns)
		st, err := os.Stat(filepath.Join(store.Dir(), id+".jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%d轮/%.1fMB", turns, float64(st.Size())/(1<<20)), func(b *testing.B) {
			ctx := context.Background()
			b.SetBytes(st.Size())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.History(ctx, id, "", "", 50); err != nil {
					b.Fatal(err)
				}
			}
		})
		_ = store.Close()
	}
}

// BenchmarkHistory翻页 模拟用户连续点「加载更早的消息」。
// 这一项最能暴露全文件扫描的问题：每点一次都重新扫一遍整个文件。
func BenchmarkHistory翻页(b *testing.B) {
	store, id, _ := buildBigSession(b, 2000)
	defer store.Close()
	ctx := context.Background()
	// 先拿到末 50 条的最旧 ID，之后从它往前翻。
	first, err := store.History(ctx, id, "", "", 50)
	if err != nil {
		b.Fatal(err)
	}
	before := first.OldestEntryID
	b.Run("单次翻页", func(b *testing.B) {
		b.SetBytes(1)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := store.History(ctx, id, "", before, 50); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkHistory连续翻页 是真实场景：打开会话后不断点「加载更早」。
//
// 与上面的「单次翻页」不同，这里每次都从不同的 before 开始，
// 而且故意在两次 History 之间不做任何事——正是用户在浏览器里的操作节奏。
// 缓存命中时省掉整个解析阶段，这是它唯一真正起作用的场景。
func BenchmarkHistory连续翻页(b *testing.B) {
	store, id, _ := buildBigSession(b, 2000)
	defer store.Close()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 每轮从头翻 10 页。
		page, err := store.History(ctx, id, "", "", 50)
		if err != nil {
			b.Fatal(err)
		}
		before := page.OldestEntryID
		for j := 0; j < 10; j++ {
			p, err := store.History(ctx, id, "", before, 50)
			if err != nil {
				b.Fatal(err)
			}
			if p.OldestEntryID == "" {
				break
			}
			before = p.OldestEntryID
		}
	}
}

// BenchmarkProjectAndGroup 测投影与回合聚合——每个 HTML 片段都要做一遍。
func BenchmarkProjectAndGroup(b *testing.B) {
	for _, turns := range []int{100, 1000} {
		store, id, _ := buildBigSession(b, turns)
		page, err := store.History(context.Background(), id, "", "", 50)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%d轮", turns), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ProjectEntries(page.Entries)
			}
		})
		_ = store.Close()
	}
}

// BenchmarkEntryHeadIsUser 单独测扫描期取「是不是 user 锚点」的那一步。
//
// 它取代了旧的 BenchmarkEntryKind：旧实现要先完整投影一条记录
// （ProjectEntries → json.Unmarshal）才能回答同一个问题，
// 而问题已经不在那条路径上了。这里的数字用于确认快路径
// 没有因为多取 role 而変重，以及大行不因此多付代价。
func BenchmarkEntryHeadIsUser(b *testing.B) {
	for _, c := range []struct {
		name string
		line string
	}{
		{"小行", `{"type":"message","id":"u1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"问题"}}`},
		{"112KB行", `{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-01-01T00:00:02.000Z","message":{"role":"assistant","content":"` + strings.Repeat("文本", 19000) + `"}}`},
	} {
		raw := []byte(c.line)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				head, ok := parseEntryHead(raw)
				if !ok {
					b.Fatal("快路径应命中")
				}
				_ = head.IsUser
			}
		})
	}
}
