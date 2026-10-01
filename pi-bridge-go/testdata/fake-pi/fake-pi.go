// Command fake-pi 是测试用的假 Pi：按脚本回帧，用于验证桥的进程与协议行为。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type frame struct {
	Type                  string `json:"type"`
	ID                    string `json:"id"`
	Success               bool   `json:"success"`
	Data                  any    `json:"data"`
	Error                 string `json:"error"`
	Method                string `json:"method,omitempty"`
	Title                 string `json:"title,omitempty"`
	AssistantMessageEvent any    `json:"assistantMessageEvent,omitempty"`
	// setStatus 帧的字段：桥的快照就靠它们（B36）。
	StatusKey  string `json:"statusKey,omitempty"`
	StatusText string `json:"statusText,omitempty"`
}

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Printf("%s\n", b)
}

func main() {
	// 可选环境断言用于验证真实 spawn 路径，不输出任何环境值。
	for _, name := range strings.Split(os.Getenv("FAKE_PI_FORBID_ENV"), ",") {
		if name == "" {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			os.Exit(70)
		}
	}
	for _, name := range strings.Split(os.Getenv("FAKE_PI_REQUIRE_ENV"), ",") {
		if name == "" {
			continue
		}
		if _, exists := os.LookupEnv(name); !exists {
			os.Exit(71)
		}
	}
	script := map[string]bool{}
	if v := os.Getenv("FAKE_PI_SCRIPT"); v != "" {
		for _, name := range strings.Split(v, ",") {
			script[strings.TrimSpace(name)] = true
		}
	}
	// 可选地把启动参数写盘，供测试断言桥确实把工具预设翻译成了 CLI 参数。
	if path := os.Getenv("FAKE_PI_ARGS_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Args, "\n")+"\n"), 0600)
	}
	// 可选地把收到的每条命令原样追加写盘（JSONL）。
	// 没有这个出口时，「某个字段到底有没有传给 Pi」只能靠推断——
	// 而此前恰好有一处参数从未到达过：桥侧以为它在传，实际被上层过滤掉了。
	cmdsFile := os.Getenv("FAKE_PI_CMDS_FILE")
	delay, _ := strconv.Atoi(envOr("FAKE_PI_DELAY_MS", "0"))
	// 按方法拖延：逗号分隔的 Pi 方法名。夹具原本只能拖慢 prompt，
	// 而验证「桥没有用统一的短超时砍断长命令」需要拖慢 compact/bash 之类。
	delayMethods := map[string]bool{}
	for _, name := range strings.Split(envOr("FAKE_PI_DELAY_METHOD", ""), ",") {
		if name = strings.TrimSpace(name); name != "" {
			delayMethods[name] = true
		}
	}
	reader := bufio.NewReader(os.Stdin)
	// script `set_status`：模拟插件在会话启动时 setStatus 一次。
	// 它发生在任何命令之前，用来验证「快照不依赖浏览器订阅」（B36）：
	// 老实现只在转发给浏览器的路径上更新快照，没订阅者就漏记。
	// script `spawn_grandchild`：起一个同进程组的后代并把 PID 写盘。
	// 用来验证桥的 Stop 回收整个进程组，而不是只杀直接子进程（B40）。
	if script["spawn_grandchild"] {
		child := exec.Command("sleep", "300")
		if err := child.Start(); err != nil {
			os.Exit(72)
		}
		if path := os.Getenv("FAKE_PI_CHILD_PID_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0600)
		}
	}
	if script["set_status"] {
		emit(frame{Type: "extension_ui_request", Method: "setStatus", StatusKey: "mc", StatusText: "mc: 3 (1%) · idle"})
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		var cmd struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Message string `json:"message"`
			// outputPath 是 export_html 的目标路径；它不在 message 里，
			// 夹具以前读错字段，于是导出成功路径从未被端到端覆盖过。
			OutputPath string `json:"outputPath"`
		}
		if json.Unmarshal([]byte(line), &cmd) != nil {
			emit(frame{Type: "response", ID: "", Success: false, Error: "无法解析命令"})
			continue
		}
		if cmdsFile != "" {
			// O_APPEND 单次写入是原子的，多个 worker 共用同一文件也不会互相截断。
			if f, err := os.OpenFile(cmdsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				_, _ = f.WriteString(line + "\n")
				_ = f.Close()
			}
		}
		if delay > 0 && delayMethods[cmd.Type] {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		switch cmd.Type {
		case "get_state":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"sessionId": sessionID(), "sessionName": "假会话", "thinkingLevel": "off",
				"isStreaming": false, "isCompacting": false, "steeringMode": "one-at-a-time",
				"followUpMode": "one-at-a-time", "autoCompactionEnabled": true,
				"messageCount": 0, "pendingMessageCount": 0, "model": nil,
			}})
		case "prompt":
			if script["replay_burst"] {
				emit(frame{Type: "agent_start"})
				emit(frame{Type: "response", ID: cmd.ID, Success: true})
				for i := 0; i < 120; i++ {
					emit(frame{Type: "message_update", AssistantMessageEvent: map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "x"}})
				}
				emit(frame{Type: "agent_settled"})
				continue
			}
			if strings.Contains(cmd.Message, "触发对话") {
				emit(frame{Type: "extension_ui_request", ID: "dialog-1", Method: "confirm", Title: "确认？"})
				// 故意不等回执：验证桥会主动取消而不是让 Pi 永久挂起。
				emit(frame{Type: "response", ID: cmd.ID, Success: true})
				continue
			}
			if script["slow_prompt"] && delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
			emit(frame{Type: "agent_start"})
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
			emit(frame{Type: "message_update", AssistantMessageEvent: map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "假回复"}})
			emit(frame{Type: "agent_settled"})
		case "extension_dialog":
			emit(frame{Type: "extension_ui_request", ID: "dialog-1", Method: "confirm", Title: "确认？"})
			// 不等回执，验证桥不会让 Pi 永久挂起。
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
		case "new_session", "switch_session", "fork", "clone":
			// 模拟 Pi 切换会话身份：返回新的 sessionId。
			next := "forked-" + sessionID()
			_ = os.Setenv("FAKE_PI_SESSION_ID", next)
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"cancelled": false, "sessionId": next, "text": "分叉出的消息",
			}})
		case "set_model":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"id": "new-model", "name": "新模型", "provider": cmd.Message,
			}})
		case "get_available_models":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"models": []map[string]any{
				{"id": "m1", "name": "模型一", "provider": "p1", "api": "anthropic-messages", "reasoning": true, "input": []string{"text"}, "contextWindow": 200000, "maxTokens": 8192},
			}}})
		case "get_available_thinking_levels":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"levels": []string{"off", "low", "high"}}})
		case "set_thinking_level", "set_steering_mode", "set_follow_up_mode", "set_auto_compaction", "set_auto_retry", "abort_retry", "set_session_name":
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
		case "compact":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"summary": "压缩摘要", "firstKeptEntryId": "a", "tokensBefore": 100, "estimatedTokensAfter": 20,
			}})
		case "get_session_stats":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"sessionId": sessionID(), "userMessages": 1, "assistantMessages": 1, "totalMessages": 2, "cost": 0.1,
			}})
		case "get_commands":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"commands": []map[string]any{
				{"name": "demo", "description": "演示", "source": "extension"},
			}}})
		case "get_tree":
			// 给一棵两层的真实形状：一个分叉点 + 两个叶子。
			// 空树会让「分支片段到底渲染成什么」无法在端到端路径上验证。
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"leafId": "a1",
				"tree": []map[string]any{{
					"entry": map[string]any{"type": "message", "id": "u1", "message": map[string]any{"role": "user", "content": "第一个问题"}},
					"children": []map[string]any{
						{"entry": map[string]any{"type": "message", "id": "a1", "label": "回答一"}, "children": []map[string]any{}},
						{"entry": map[string]any{"type": "message", "id": "a1b", "label": "分支回答"}, "children": []map[string]any{}},
					},
				}},
			}})
		case "get_entries":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"entries": []any{}, "leafId": "a"}})
		case "get_fork_messages":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"messages": []map[string]any{
				{"entryId": "a", "text": "第一条"},
			}}})
		case "get_last_assistant_text":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"text": "最后的回复"}})
		case "cycle_model", "cycle_thinking_level":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: nil})
		case "bash":
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"output": "假输出", "exitCode": 0, "cancelled": false, "truncated": false,
			}})
		case "abort_bash":
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
		case "abort", "steer", "follow_up":
			// 桥忽略这三个的返回值（只看成败）；steer/follow_up 的参数
			// 由 FAKE_PI_CMDS_FILE 记录，测试从那里断言真实载荷。
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
		case "clear_queue":
			// 脚本 `queued` 时让队列非空，用来验证「中止前先把排队消息取回」
			// 这条交互（Pi 的 clear_queue 返回被移除的消息文本）。
			if script["queued"] {
				emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
					"steering": []string{"排队中的引导"}, "followUp": []string{"排队中的后续"},
				}})
				continue
			}
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{
				"steering": []string{}, "followUp": []string{},
			}})
		case "export_html":
			// 回显请求里的 outputPath，让桥能校验路径一致性。
			// 可选地真写一个文件（FAKE_PI_EXPORT_BYTES），供配额相关的测试使用。
			if n, _ := strconv.Atoi(envOr("FAKE_PI_EXPORT_BYTES", "0")); n > 0 && cmd.OutputPath != "" {
				if err := os.WriteFile(cmd.OutputPath, make([]byte, n), 0600); err != nil {
					emit(frame{Type: "response", ID: cmd.ID, Success: false, Error: "写入导出文件失败"})
					continue
				}
			}
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"path": cmd.OutputPath}})
		case "crash":
			os.Exit(3)
		default:
			// 严格失败而不是静默成功：假 Pi 以前对任何未知方法都回 success，
			// 于是「桥调用了 Pi 根本不存在的命令」在测试里看不出来——
			// 方法名拼错、旧协议残留都会被当作正常工作。
			// 补齐后会暴露夹具漏实现的方法（clear_queue 就是这样发现的），
			// 补实现即可；真实 Pi 对它不认识的方法正是这样回错的。
			emit(frame{Type: "response", ID: cmd.ID, Success: false, Error: "unknown method: " + cmd.Type})
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func sessionID() string {
	if v := os.Getenv("FAKE_PI_SESSION_ID"); v != "" {
		return v
	}
	return "fake-session"
}
