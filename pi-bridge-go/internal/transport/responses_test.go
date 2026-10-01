package transport

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// 回执形状的线上契约。
//
// 这些键名是前端按名读取、api/v1/protocol.md 描述的东西。以前它们是
// 散在三十多个 return 上的 map 字面量，改错一个键不会有任何东西发现；
// 现在每个形状有具名类型，这张表把「哪个方法给哪些键」钉住。
//
// 加键要同时改这里与 protocol.md；这是有意的重复——测试的职责就是
// 独立重述期望，而不是从实现里推导出来。
func Test回执形状(t *testing.T) {
	cases := []struct {
		methods []string
		value   any
		keys    []string
	}{
		{[]string{"session.set_queue_mode"}, queueModeReply{}, []string{"kind", "mode"}},
		{[]string{"session.steer", "session.follow_up"}, queuedReply{}, []string{"queued"}},
		{[]string{"session.set_auto_compaction", "session.set_auto_retry"}, enabledReply{}, []string{"enabled"}},
		{[]string{"session.abort_retry", "session.abort_bash"}, abortedReply{}, []string{"aborted"}},
		{[]string{"session.prompt"}, acceptedReply{}, []string{"accepted"}},
		{[]string{"session.abort"}, clearedQueueReply{}, []string{"clearedQueue"}},
		{[]string{"session.stop"}, stoppedReply{}, []string{"stopped"}},
		{[]string{"session.set_thinking", "session.cycle_thinking"}, levelReply{}, []string{"level"}},
		{[]string{"session.set_name"}, renamedReply{}, []string{"renamed"}},
		{[]string{"session.last_assistant"}, textReply{}, []string{"text"}},
		{[]string{"session.bash_output"}, textTruncatedReply{}, []string{"text", "truncated"}},
		{[]string{"session.new", "session.switch", "session.clone"}, sessionIDReply{}, []string{"sessionId"}},
		{[]string{"session.export_html"}, exportReply{}, []string{"path"}},
		{[]string{"config.models.write", "terminal.input"}, writtenReply{}, []string{"written"}},
		{[]string{"terminal.resize"}, resizedReply{}, []string{"resized"}},
		{[]string{"terminal.close"}, closedReply{}, []string{"closed"}},
		{[]string{"session.ui_response"}, answeredReply{}, []string{"answered"}},
		{[]string{"config.models.discover", "config.catalog"}, modelsReply{}, []string{"models"}},
		{[]string{"config.packages"}, packagesReply{}, []string{"packages"}},
		{[]string{"terminal.open"}, terminalOpenedReply{},
			[]string{"terminalId", "cwd", "pid", "cols", "rows"}},
		{[]string{"terminal.list"}, terminalsReply{}, []string{"terminals"}},
		{[]string{"files.list"}, entriesReply{}, []string{"entries", "truncated"}},
		{[]string{"files.read"}, fileTextReply{}, []string{"text", "truncated", "size"}},
		{[]string{"files.image"}, fileImageReply{}, []string{"mime", "data", "size"}},
		{[]string{"files.roots"}, rootsReply{}, []string{"roots"}},
		{[]string{"git.diff"}, diffReply{}, []string{"diff", "truncated"}},
		{[]string{"session.pending_dialogs"}, dialogsReply{}, []string{"dialogs", "ids"}},
		// delete 的基础形状来自 sessions.DeleteResult，多一个可选标记。
		{[]string{"sessions.delete"}, deleteReply{}, []string{"sessionId", "trashed", "path"}},
		// 事件载荷（kind=event / control 的 data）。
		{[]string{"event bridge.terminal_closed"}, terminalClosedEvent{}, []string{"terminalId"}},
		{[]string{"event terminal.output"}, terminalOutputEvent{}, []string{"terminalId", "data"}},
	}
	for _, c := range cases {
		for _, method := range c.methods {
			b, err := json.Marshal(c.value)
			if err != nil {
				t.Fatalf("%s: 序列化失败：%v", method, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(b, &fields); err != nil {
				t.Fatalf("%s: 输出不是 JSON 对象：%s", method, b)
			}
			got := make([]string, 0, len(fields))
			for k := range fields {
				got = append(got, k)
			}
			sort.Strings(got)
			want := append([]string(nil), c.keys...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s 的回执键为 %v，期望 %v", method, got, want)
			}
		}
	}
}

// 停掉运行中会话时多出的那个标记必须真的出现，且只在为真时出现。
// 这条单独测是因为它带 omitempty，零值形状本身看不出区别。
func Test删除回执的可选标记(t *testing.T) {
	b, err := json.Marshal(deleteReply{StoppedWorker: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"stoppedWorker":true`) {
		t.Fatalf("连带停掉会话时必须带 stoppedWorker: %s", b)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("应为 4 个键，得到 %v", keysOf(fields))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
