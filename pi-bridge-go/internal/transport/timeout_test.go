package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Test长任务命令不被默认超时砍断 覆盖 B66：
// 全部命令曾共用 OperationTimeout（默认 30 秒），长压缩、session.bash
// 这类会被桥中途放弃，而 Pi 其实还在跑——结果丢失、前端只看到失败。
//
// 把默认超时压到 300ms，再让 fake-pi 对两个方法都延迟 900ms：
// 标注了长超时的方法应当照常完成，没标注的按默认超时失败。
// 两侧必须同时成立，否则「按方法区分」会退化成「整体调大」。
func Test长任务命令不被默认超时砍断(t *testing.T) {
	t.Setenv("FAKE_PI_DELAY_MS", "900")
	t.Setenv("FAKE_PI_DELAY_METHOD", "compact,get_session_stats")
	s, _, cwd := newTestServerTuned(t, 300*time.Millisecond)

	var mu sync.Mutex
	var sent [][]byte
	bridge := NewTunnelBridge(s, func(frame []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, frame)
		return nil
	}, 4, 5*time.Minute)
	s.SetTunnelBridge(bridge)
	t.Cleanup(bridge.Close)

	boot := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "boot",
		"method": "session.start", "params": map[string]any{"cwd": cwd},
	})
	if !bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", boot)) {
		t.Fatal("隧道帧未被处理")
	}
	waitFrames(t, &mu, &sent, 1)
	started := decodeFrame(t, lastFrame(t, &mu, &sent))
	data, _ := started["data"].(map[string]any)
	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("会话启动未返回 sessionId: %v", started)
	}

	// 标注了长超时的命令：900ms 延迟不该被 300ms 的默认超时砍断。
	compact := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "c1", "sessionId": sessionID,
		"method": "session.compact",
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", compact))
	waitFrames(t, &mu, &sent, 2)
	if reply := decodeFrame(t, lastFrame(t, &mu, &sent)); reply["ok"] != true {
		t.Fatalf("长任务被默认超时打断: %v", reply)
	}

	// 同一延迟、没有标注的方法：仍受默认超时约束。
	// 换成 session.stats 而不是 session.state：后者在会话启动路径上，
	// 拖慢它会连 session.start 一起弄超时，测不出想测的东西。
	state := mustFrame(t, map[string]any{
		"version": 1, "kind": "command", "requestId": "s1", "sessionId": sessionID,
		"method": "session.stats",
	})
	bridge.HandleFrame(context.Background(), wrapFrom(t, "tab-1", state))
	waitFrames(t, &mu, &sent, 3)
	reply := decodeFrame(t, lastFrame(t, &mu, &sent))
	if reply["ok"] != false {
		t.Fatalf("未标注的方法应受默认超时约束，实际: %v", reply)
	}
}
