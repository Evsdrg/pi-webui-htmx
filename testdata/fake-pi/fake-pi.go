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
