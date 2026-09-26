// Command smoke-client 只做 A 阶段冒烟：握手、查询状态、停止，不发送提示词。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:30142", "桥监听地址")
	token := flag.String("token", "", "Bearer token")
	cwd := flag.String("cwd", "", "启动会话的工作目录")
	hold := flag.Bool("hold", false, "启动后不停止，用于测量工作态内存")
	dialog := flag.Bool("dialog", false, "触发扩展对话并持续读事件")
	flag.Parse()
	if *token == "" {
		fmt.Fprintln(os.Stderr, "缺少 --token")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+*token)
	conn, _, err := websocket.Dial(ctx, "ws://"+*addr+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接失败:", err)
		os.Exit(1)
	}
	defer conn.CloseNow()
	send := func(v map[string]any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			fmt.Fprintln(os.Stderr, "发送失败:", err)
			os.Exit(1)
		}
	}
	read := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取失败:", err)
			os.Exit(1)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-1", "method": "worker.list"})
	fmt.Println("worker.list:", compact(read()))
	if *cwd == "" {
		return
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-2", "method": "session.start", "params": map[string]any{"cwd": *cwd}})
	started := read()
	fmt.Println("session.start:", compact(started))
	data, _ := started["data"].(map[string]any)
	id, _ := data["sessionId"].(string)
	if id == "" {
		fmt.Fprintln(os.Stderr, "启动未返回 sessionId")
		os.Exit(1)
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-3", "sessionId": id, "method": "session.state"})
	fmt.Println("session.state:", compact(read()))
	if *dialog {
		// 触发扩展对话：订阅后发 prompt，让假 Pi 推来 select 请求。
		send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-sub", "sessionId": id, "method": "session.subscribe"})
		if r := read(); r["ok"] != true {
			fmt.Println("订阅失败:", compact(r))
			os.Exit(1)
		}
		send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-prompt", "sessionId": id, "method": "session.prompt", "params": map[string]any{"text": "触发对话"}})
		fmt.Println("session.prompt:", compact(read()))
		// 打印 sessionId 供外部脚本查询 HTTP 端点。
		fmt.Println("SESSION_ID=" + id)
		// 持续读事件，直到上下文结束。
		for {
			m := read()
			if m == nil {
				break
			}
			if ev, _ := m["event"].(string); ev == "pi.event" {
				fmt.Println("event:", compact(m["data"]))
			}
		}
		return
	}
	if *hold {
		fmt.Println("持有中，不发送提示词，按 Ctrl+C 或超时退出")
		<-ctx.Done()
		return
	}
	send(map[string]any{"version": 1, "kind": "command", "requestId": "smoke-4", "sessionId": id, "method": "session.stop", "params": map[string]any{"force": true}})
	fmt.Println("session.stop:", compact(read()))
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
