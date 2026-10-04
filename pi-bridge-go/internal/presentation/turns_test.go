package presentation

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pi-bridge-go/internal/sessions"
)

// thinking 造一条纯思考块，方便拼测试条目。
func thinking(index int) sessions.LazyBlock {
	return sessions.LazyBlock{Kind: "thinking", BlockIndex: index}
}

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

// assistantEntry 造一条带思考块的 assistant 条目。
func assistantEntry(id string, blocks ...int) sessions.Entry {
	lazy := []sessions.LazyBlock{}
	for _, b := range blocks {
		lazy = append(lazy, sessions.LazyBlock{Kind: "thinking", BlockIndex: b})
	}
	return sessions.Entry{ID: id, Kind: sessions.KindAssistant, Text: "回答 " + id, Lazy: lazy}
}

// flowThinking 按时间顺序压平回合内全部工作段的思考占位符。
func flowThinking(turn Turn) []ThinkingBlock {
	var out []ThinkingBlock
	for _, block := range turn.Flow {
		for _, item := range block.Items {
			if item.Kind == "thinking" {
				out = append(out, item.Thinking)
			}
		}
	}
	return out
}

// flowText 按时间顺序拼接回合内全部正文段。
func flowText(turn Turn) string {
	var parts []string
	for _, block := range turn.Flow {
		if block.Kind == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// flowSummaries 拼接全部工作段摘要，供计数与耗时断言。
func flowSummaries(turn Turn) string {
	var parts []string
	for _, block := range turn.Flow {
		if block.Kind == "work" && block.Summary != "" {
			parts = append(parts, block.Summary)
		}
	}
	return strings.Join(parts, " | ")
}

// flowKinds 返回回合的块类型序列（"text"/"work"），用于顺序断言。
func flowKinds(turn Turn) []string {
	out := make([]string, 0, len(turn.Flow))
	for _, block := range turn.Flow {
		out = append(out, block.Kind)
	}
	return out
}

func Test搜索条目均能定位所属回合(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "提问"},
		assistantEntry("a1"),
		{ID: "tool1", Kind: sessions.KindTool, Text: "工具输出"},
		assistantEntry("a2"),
		{ID: "comp1", Kind: sessions.KindCompaction, Text: "压缩摘要"},
		assistantEntry("solo"),
	})
	if len(turns) != 2 || !slices.Equal(turns[0].EntryIDs, []string{"u1", "a1", "tool1", "a2"}) || !slices.Equal(turns[1].EntryIDs, []string{"comp1", "solo"}) {
		t.Fatalf("搜索命中的原始条目找不到所属回合: %+v", turns)
	}
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
	got := flowThinking(turns[0])
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
	got := flowThinking(turns[0])
	if len(got) != 1 || got[0].EntryID != "solo" || got[0].BlockIndex != 2 {
		t.Fatalf("孤儿回合占位符归属错误: %+v", got)
	}
}

func Test失败回合保留用户图片和安全错误(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "颜色？", Lazy: []sessions.LazyBlock{{Kind: "image", BlockIndex: 1}}},
		{ID: "a1", Kind: sessions.KindAssistant, Error: "模型请求失败：供应商余额不足"},
	})
	if len(turns) != 1 || len(turns[0].UserImages) != 1 || turns[0].UserImages[0] != (ImageBlock{EntryID: "u1", BlockIndex: 1}) || turns[0].Error == "" {
		t.Fatalf("失败回合缺少图片或错误提示: %+v", turns)
	}
	turns = GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "颜色？"},
		{ID: "a1", Kind: sessions.KindAssistant, Error: "模型请求失败"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "红蓝"},
	})
	if len(turns) != 1 || turns[0].Error != "" || flowText(turns[0]) != "红蓝" {
		t.Fatalf("自动恢复成功后不应继续显示上一次失败: %+v", turns)
	}
}

// Test没有思考块时不产生占位符 覆盖反向边界。
func Test没有思考块时不产生占位符(t *testing.T) {
	entries := []sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "回答"},
	}
	turns := GroupTurns(entries)
	if len(turns) != 1 || len(flowThinking(turns[0])) != 0 {
		t.Fatalf("不应产生占位符: %+v", turns)
	}
}

// 工作段摘要必须一次说清工具步骤数、思考段数与失败数，
// 这样工作段折叠时用户仍知道里面发生了什么、有没有失败。
func Test过程摘要计数工具思考失败(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		{ID: "a1", Kind: sessions.KindAssistant, Lazy: []sessions.LazyBlock{thinking(0)}},
		{ID: "t1", Kind: sessions.KindTool, Text: "ok", ToolName: "bash"},
		{ID: "t2", Kind: sessions.KindTool, Text: "bad", ToolName: "grep", Failed: true},
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	got := flowSummaries(turns[0])
	// 无时间戳的回合以「已处理」开头（ZCode 无时长时的说法）。
	for _, want := range []string{"已处理", "2 个工具", "1 段思考", "1 个失败"} {
		if !strings.Contains(got, want) {
			t.Fatalf("过程摘要缺少 %q：%q", want, got)
		}
	}
}

// 纯思考回合也要有过程组：没有工具但有思考时摘要只报思考段数。
func Test纯思考回合有过程摘要(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		assistantEntry("a1", 0, 1),
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	got := flowSummaries(turns[0])
	if got == "" {
		t.Fatal("纯思考回合必须有过程摘要（否则模板不渲染过程组）")
	}
	if !strings.Contains(got, "2 段思考") {
		t.Fatalf("摘要应报 2 段思考：%q", got)
	}
	if strings.Contains(got, "工具") {
		t.Fatalf("纯思考回合不应出现工具计数：%q", got)
	}
}

// 纯正文回合没有工作段：Flow 里只有正文块，模板据此不渲染空折叠组。
func Test纯正文回合无过程摘要(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问题"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "回答"},
	})
	if len(turns) != 1 || len(turns[0].Flow) != 1 || turns[0].Flow[0].Kind != "text" {
		t.Fatalf("纯正文回合不应有工作段: %+v", turns)
	}
	if flowSummaries(turns[0]) != "" {
		t.Fatalf("纯正文回合不应有摘要: %q", flowSummaries(turns[0]))
	}
}

// 每段工作的时间范围取段内各项的起点与终点，绝不把各步时长求和：
// 并行工具与同一助手上的多段思考都会让「求和」重复计数。
// 这里工作段 2（两个工具 + 一段思考）各项时长和为 2+2+8=12 秒，
// 而真实范围是 10s→20s，只能报 10 秒。
func Test工作段耗时取时间范围不求和(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "跑", Timestamp: at("2026-09-21T13:33:00Z")},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "好", Timestamp: at("2026-09-21T13:33:10Z"), Lazy: []sessions.LazyBlock{thinking(0)}},
		{ID: "t1", Kind: sessions.KindTool, Text: "a", Timestamp: at("2026-09-21T13:33:12Z"), ToolName: "bash"},
		{ID: "t2", Kind: sessions.KindTool, Text: "b", Timestamp: at("2026-09-21T13:33:12Z"), ToolName: "grep"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "完成", Timestamp: at("2026-09-21T13:33:20Z"), Lazy: []sessions.LazyBlock{thinking(0)}},
		{ID: "t3", Kind: sessions.KindTool, Text: "c", Timestamp: at("2026-09-21T13:33:22Z"), ToolName: "read"},
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	got := flowSummaries(turns[0])
	if !strings.Contains(got, "已工作 10 秒") || !strings.Contains(got, "已工作 2 秒") {
		t.Fatalf("工作段耗时应取段内时间范围（10 秒 / 2 秒）：%q", got)
	}
	for _, bad := range []string{"12 秒", "24 秒", "18 秒", "6 秒"} {
		if strings.Contains(got, bad) {
			t.Fatalf("工作段耗时不得由各步时长相加（出现 %q）：%q", bad, got)
		}
	}
}

// 时间不可靠（任一必需端缺失）时不显示耗时，而不是拿零值当 1970 年；
// 摘要退回「已处理」（ZCode 在无时长时同样只说结果）。
func Test过程耗时不完整时不显示(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "跑"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "好", Lazy: []sessions.LazyBlock{thinking(0)}},
		{ID: "t1", Kind: sessions.KindTool, Text: "a", ToolName: "bash"},
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	got := flowSummaries(turns[0])
	if !strings.Contains(got, "已处理") {
		t.Fatalf("时间缺失时应以「已处理」开头：%q", got)
	}
	for _, bad := range []string{"秒", "分", "时"} {
		if strings.Contains(got, bad) {
			t.Fatalf("时间缺失时摘要不应带耗时（出现 %q）：%q", bad, got)
		}
	}
}

// 空/纯空白正文与提供商丢弃标记都不渲染成气泡：
// 前者会留下一块看不见内容的气泡，后者会把 "[dropped ]" 当正文展示。
func Test空白正文与丢弃标记不渲染(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "\n  \n"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "[dropped ]"},
		{ID: "a3", Kind: sessions.KindAssistant, Text: "真正的正文。[dropped ]"},
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	if got := flowText(turns[0]); got != "真正的正文。" {
		t.Fatalf("空白与标记段应被拦掉、结尾标记应被剥离：%q", got)
	}
}

// 丢弃标记只在结尾剥离：正文中间出现的疑似字样是内容，不是标记。
func Test丢弃标记只在结尾剥离(t *testing.T) {
	if got := stripDroppedMarker("讨论 [dropped ] 这个词时不要动它"); got != "讨论 [dropped ] 这个词时不要动它" {
		t.Fatalf("正文中间不应被剥离：%q", got)
	}
	if got := stripDroppedMarker("好的，继续。\n[dropped ]"); got != "好的，继续。" {
		t.Fatalf("结尾标记应被剥离：%q", got)
	}
	if got := stripDroppedMarker("[dropped ]"); got != "" {
		t.Fatalf("整段只有标记时应剥成空串：%q", got)
	}
}

// 时长的中文写法：尾段为零时不出现（与 ZCode 一致）。
func Test工作时长格式化(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{5, "5 秒"},
		{59, "59 秒"},
		{60, "1 分"},
		{61, "1 分 1 秒"},
		{77, "1 分 17 秒"},
		{3600, "1 时"},
		{3660, "1 时 1 分"},
		{7325, "2 时 2 分"},
	}
	for _, c := range cases {
		if got := formatWorkDuration(c.seconds); got != c.want {
			t.Fatalf("formatWorkDuration(%d) = %q，期望 %q", c.seconds, got, c.want)
		}
	}
}

// 模型会在工具调用之间穿插说明性正文；这些正文段必须留在各自的时间位置，
// 而不是全部拼到回合末尾（曾经单字段拼接就是这样，时间线读起来是错的）。
func Test正文段按时间顺序落在工作段之间(t *testing.T) {
	turns := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "跑"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "先看看目录"},
		{ID: "t1", Kind: sessions.KindTool, Text: "输出一", ToolName: "read"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "再改配置", Lazy: []sessions.LazyBlock{thinking(0)}},
		{ID: "t2", Kind: sessions.KindTool, Text: "输出二", ToolName: "edit"},
		{ID: "a3", Kind: sessions.KindAssistant, Text: "结论"},
	})
	if len(turns) != 1 {
		t.Fatalf("应只有一个回合: %d", len(turns))
	}
	turn := turns[0]
	if got := flowKinds(turn); !slices.Equal(got, []string{"text", "work", "text", "work", "text"}) {
		t.Fatalf("块序列应为 正文/工作/正文/工作/正文：%v", got)
	}
	// 三段正文各在各位：拼接顺序即时间顺序。
	if got := flowText(turn); got != "先看看目录\n\n再改配置\n\n结论" {
		t.Fatalf("正文段顺序错误：%q", got)
	}
	// 第一段工作含 t1 与 a2 的思考（t1 之后没有正文隔开，同属一段）；
	// 第二段工作含 t2。
	work1, work2 := turn.Flow[1], turn.Flow[3]
	if len(work1.Items) != 2 || work1.Items[0].Kind != "tool" || work1.Items[0].Step.Detail != "输出一" || work1.Items[1].Kind != "thinking" {
		t.Fatalf("第一段工作内容或顺序错误：%+v", work1.Items)
	}
	if len(work2.Items) != 1 || work2.Items[0].Step.Detail != "输出二" {
		t.Fatalf("第二段工作内容错误：%+v", work2.Items)
	}
	// 相邻正文段合并：重试等场景下同一段话分条写入，不该出现两个气泡。
	merged := GroupTurns([]sessions.Entry{
		{ID: "u1", Kind: sessions.KindUser, Text: "问"},
		{ID: "a1", Kind: sessions.KindAssistant, Text: "前半"},
		{ID: "a2", Kind: sessions.KindAssistant, Text: "后半"},
	})
	if got := flowText(merged[0]); got != "前半\n\n后半" || len(merged[0].Flow) != 1 {
		t.Fatalf("相邻正文段应合并为一块：%q %d", got, len(merged[0].Flow))
	}
}
