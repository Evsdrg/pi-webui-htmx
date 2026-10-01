package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"pi-bridge-go/internal/testutil"
)

// Test排队方式命令端到端 覆盖 session.set_queue_mode 的完整链路。
//
// 这条命令此前在桥侧**没有任何测试**（只有回执结构的形状测试与协议表里的
// 方法名）：dispatch 里解析参数、调 SetQueueMode、拼回执的整段路径零覆盖，
// 把成功回执整个吞掉的改动（例如条件写反）不会被任何测试发现。
//
// 同时验证 kind 的翻译：Pi 的方法名是 set_steering_mode / set_follow_up_mode，
// 而前端发的是 steering / followUp。旧前端曾发 steer——它必须被明确拒绝
// 而不是静默当成某种默认值，否则用户设了「一次一个」却在按另一套行为跑。
func Test排队方式命令端到端(t *testing.T) {
	cmdsFile := filepath.Join(t.TempDir(), "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmdsFile)

	s, _, cwd := newTestServer(t)
	srv := httptest.NewUnstartedServer(s)
	s.host = srv.Listener.Addr().String()
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+testToken)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	defer conn.CloseNow()

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			t.Fatalf("响应不是 JSON: %s", b)
		}
		return m
	}

	send(map[string]any{"version": 1, "kind": "command", "requestId": "q-boot", "method": "session.start", "params": map[string]any{"cwd": cwd}})
	started := read()
	startData, _ := started["data"].(map[string]any)
	sessionID, _ := startData["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("启动响应缺少 sessionId: %v", started)
	}

	// ① 成功路径：回执必须回显 kind 与 mode（前端按它更新选中态）。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "q1", "sessionId": sessionID,
		"method": "session.set_queue_mode", "params": map[string]any{"kind": "steering", "mode": "all"}})
	reply := read()
	if reply["ok"] != true {
		t.Fatalf("设置排队方式失败: %v", reply)
	}
	data, _ := reply["data"].(map[string]any)
	if data["kind"] != "steering" || data["mode"] != "all" {
		t.Fatalf("回执应回显 kind 与 mode: %v", reply)
	}
	// 桥侧翻译：steering 对应 Pi 的 set_steering_mode。
	payload := testutil.WaitForCommand(t, cmdsFile, "set_steering_mode")
	if payload["mode"] != "all" {
		t.Fatalf("Pi 收到的 mode 不对: %v", payload)
	}

	// ② 旧前端的错误值 steer：必须明确拒绝，不能当成默认值放过去。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "q2", "sessionId": sessionID,
		"method": "session.set_queue_mode", "params": map[string]any{"kind": "steer", "mode": "all"}})
	if reply := read(); reply["ok"] == true {
		t.Fatalf("kind=steer 应被拒绝: %v", reply)
	}

	// ③ 非法 mode：同样要拒绝。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "q3", "sessionId": sessionID,
		"method": "session.set_queue_mode", "params": map[string]any{"kind": "steering", "mode": "bogus"}})
	if reply := read(); reply["ok"] == true {
		t.Fatalf("非法 mode 应被拒绝: %v", reply)
	}

	// ④ followUp 走另一条 Pi 方法，别把两个 kind 映射到同一个方法上。
	send(map[string]any{"version": 1, "kind": "command", "requestId": "q4", "sessionId": sessionID,
		"method": "session.set_queue_mode", "params": map[string]any{"kind": "followUp", "mode": "one-at-a-time"}})
	if reply := read(); reply["ok"] != true {
		t.Fatalf("followUp 设置失败: %v", reply)
	}
	payload = testutil.WaitForCommand(t, cmdsFile, "set_follow_up_mode")
	if payload["mode"] != "one-at-a-time" {
		t.Fatalf("Pi 收到的 followUp mode 不对: %v", payload)
	}
}
