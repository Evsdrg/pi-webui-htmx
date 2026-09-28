// Command fake-pi 是测试用的假 Pi：按脚本回帧，用于验证桥的进程与协议行为。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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
	delay, _ := strconv.Atoi(envOr("FAKE_PI_DELAY_MS", "0"))
	reader := bufio.NewReader(os.Stdin)
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
		}
		if json.Unmarshal([]byte(line), &cmd) != nil {
			emit(frame{Type: "response", ID: "", Success: false, Error: "无法解析命令"})
			continue
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
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"tree": []any{}, "leafId": "a"}})
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
		case "export_html":
			// 回显请求里的 outputPath，让桥能校验路径一致性。
			emit(frame{Type: "response", ID: cmd.ID, Success: true, Data: map[string]any{"path": cmd.Message}})
		case "crash":
			os.Exit(3)
		default:
			emit(frame{Type: "response", ID: cmd.ID, Success: true})
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
