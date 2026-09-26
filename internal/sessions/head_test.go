package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseEntryHead边界 覆盖早停解析的所有分支。
//
// 这个函数是 History 扫描阶段的快路径，返回值直接决定条目是否入索引，
// 所以「认不出来时回退」和「认出来时正确」两条路都要钉死。
func TestParseEntryHead边界(t *testing.T) {
	cases := []struct {
		name string
		line string
		want entryHead
		ok   bool
	}{
		{
			name: "标准形状",
			line: `{"type":"message","id":"u1","parentId":"p1","message":{"x":1}}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, Parent: "p1"},
			ok:   true,
		},
		{
			name: "parentId 为 null",
			line: `{"type":"message","id":"u1","parentId":null,"message":{}}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, ParentNull: true},
			ok:   true,
		},
		{
			name: "字段顺序颠倒",
			line: `{"message":{"x":1},"parentId":"p1","id":"u1","type":"message"}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, Parent: "p1"},
			ok:   true,
		},
		{
			name: "嵌套对象里有同名字段",
			line: `{"type":"message","id":"u1","parentId":"p1","message":{"type":"other","id":"nested","parentId":"deep"}}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, Parent: "p1"},
			ok:   true,
		},
		{
			name: "嵌套数组里有同名字段",
			line: `{"type":"message","id":"u1","parentId":"p1","message":[{"id":"a"},{"id":"b"}]}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, Parent: "p1"},
			ok:   true,
		},
		{
			name: "大字段在后面（真实 Pi 的顺序）",
			line: `{"type":"message","id":"u1","parentId":null,"timestamp":"t","message":{"content":"` + strings.Repeat("x", 5000) + `"}}`,
			want: entryHead{Type: "message", ID: "u1", HasParent: true, ParentNull: true},
			ok:   true,
		},
		{
			name: "ID 含转义",
			line: `{"type":"message","id":"a\"b","parentId":"p\\1"}`,
			want: entryHead{Type: "message", ID: `a"b`, HasParent: true, Parent: `p\1`},
			ok:   true,
		},
		{
			name: "ID 含 unicode 转义",
			line: `{"type":"message","id":"\u00e9","parentId":null}`,
			want: entryHead{Type: "message", ID: "é", HasParent: true, ParentNull: true},
			ok:   true,
		},
		{
			name: "缺 parentId",
			line: `{"type":"message","id":"u1"}`,
			ok:   false,
		},
		{
			name: "缺 type",
			line: `{"id":"u1","parentId":null}`,
			ok:   false,
		},
		{
			name: "缺 id",
			line: `{"type":"message","parentId":null}`,
			ok:   false,
		},
		{
			name: "type 不是字符串",
			line: `{"type":123,"id":"u1","parentId":null}`,
			ok:   false,
		},
		{
			name: "parentId 是数字",
			line: `{"type":"message","id":"u1","parentId":123}`,
			ok:   false,
		},
		{
			name: "parentId 是对象",
			line: `{"type":"message","id":"u1","parentId":{}}`,
			ok:   false,
		},
		{
			name: "顶层不是对象",
			line: `["type","id"]`,
			ok:   false,
		},
		{
			name: "JSON 损坏",
			line: `{"type":"message","id":`,
			ok:   false,
		},
		{
			name: "空输入",
			line: ``,
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseEntryHead([]byte(c.line))
			if ok != c.ok {
				t.Fatalf("ok = %v，期望 %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Fatalf("got %+v，期望 %+v", got, c.want)
			}
		})
	}
}

// TestParseEntryHead与整体解析一致 用随机但合法的记录交叉验证：
// 只要快路径返回 ok，它给出的三个字段必须与整体 Unmarshal 完全一致。
func TestParseEntryHead与整体解析一致(t *testing.T) {
	lines := []string{
		`{"type":"message","id":"u1","parentId":"p1","message":{"role":"user","content":"hi"}}`,
		`{"type":"custom","customType":"x","data":{"a":[1,2,{"id":"deep"}]},"id":"c1","parentId":null}`,
		`{"type":"thinking_level_change","id":"t1","parentId":"c1","thinkingLevel":"high"}`,
		`{"type":"message","id":"m1","parentId":"t1","timestamp":"2026-01-01T00:00:00Z","message":{"role":"toolResult","content":[{"type":"text","text":"` + strings.Repeat("y", 3000) + `"}]}}`,
	}
	for _, line := range lines {
		head, ok := parseEntryHead([]byte(line))
		if !ok {
			t.Fatalf("快路径应成功: %s", line[:min(60, len(line))])
		}
		var item struct {
			Type   string          `json:"type"`
			ID     string          `json:"id"`
			Parent json.RawMessage `json:"parentId"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		if head.Type != item.Type || head.ID != item.ID {
			t.Fatalf("字段不一致: 快路径 %+v vs 整体 %+v", head, item)
		}
		// parentId 的三种形态逐一对应。
		switch string(item.Parent) {
		case "null":
			if !head.ParentNull || head.Parent != "" {
				t.Fatalf("null 处理错误: %+v", head)
			}
		default:
			if head.ParentNull {
				t.Fatalf("不应判定为 null: %+v", head)
			}
			var want string
			if err := json.Unmarshal(item.Parent, &want); err != nil {
				t.Fatal(err)
			}
			if head.Parent != want {
				t.Fatalf("parent 不一致: %q vs %q", head.Parent, want)
			}
		}
	}
}

// TestHistory拒绝重复顶层键 记录一个已知的语义差异。
//
// 整体 Unmarshal 对重复键取最后一个，早停取第一个后即停。
// Pi 的写入方是结构体序列化，不可能产生重复键；且下面的用例说明
// 真正的重复 id 会被既有校验抓住，不会静默放进索引。
func TestHistory拒绝重复顶层键(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	defer store.Close()
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"dup","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"a","parentId":null}`,
		`{"type":"message","id":"a","parentId":"a"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dup.jsonl"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := store.History(context.Background(), "dup", "", "", 50)
	if err == nil {
		t.Fatal("重复条目 ID 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Fatalf("错误原因不对: %v", err)
	}
}

// TestHistory字段顺序异常时回退慢路径 确认快路径认不出来时行为不变。
func TestHistory字段顺序异常时回退慢路径(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	defer store.Close()
	// parentId 是数字：快路径拒绝，慢路径必须给出「父条目 ID 无效」。
	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"odd","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"a","parentId":123}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "odd.jsonl"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := store.History(context.Background(), "odd", "", "", 50)
	if err == nil || !strings.Contains(err.Error(), "父条目 ID 无效") {
		t.Fatalf("应回退慢路径并报父条目错误，实际: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
