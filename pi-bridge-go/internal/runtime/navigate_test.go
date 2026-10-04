// 会话内跳转（「从此处编辑」的桥侧通道）与删除会话的强制停止。
//
// 背景：pi 的 RPC 命令表没有 navigate_tree，唯一的触达通道是「prompt 文本
// 命中扩展命令」——桥随进程下发一个内部扩展，命令结果经结果文件回传。
// 删除会话则必须能把 force 真正传导到忙 worker 上，否则「运行中的会话
// 删不掉」且界面上看不出原因。
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// navTestConfig 给夹具打开跳转通道：扩展路径只用于「已启用」判定，
// fake-pi 不加载它；结果目录用来接收 fake-pi 写回的结果文件
// （fake-pi 读 PI_WEBUI_NAV_RESULT 定位结果文件，与桥内扩展同名）。
func navTestConfig(t *testing.T, resultDir string) func(*Config) {
	return func(c *Config) {
		c.NavigateExt = filepath.Join(t.TempDir(), "bridge-navigate.mjs")
		c.NavigateResultDir = resultDir
	}
}

func TestNavigate跳转到用户消息回到其父节点(t *testing.T) {
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmds)
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	reply, err := w.Navigate(context.Background(), "u1")
	if err != nil {
		t.Fatalf("跳转失败: %v", err)
	}
	if reply.LeafID != "" || reply.PreviousLeafID != "a1" {
		t.Fatalf("跳转结果异常: %+v（用户消息应回到父节点，根用空串表示）", reply)
	}
	body, rerr := os.ReadFile(cmds)
	if rerr != nil || !strings.Contains(string(body), `"/pi-webui-navigate u1"`) {
		t.Fatalf("pi 未收到跳转命令: %v %q", rerr, string(body))
	}
}

func TestNavigate非用户目标落在目标自身(t *testing.T) {
	dir := t.TempDir()
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	reply, err := w.Navigate(context.Background(), "a1")
	if err != nil {
		t.Fatalf("跳转失败: %v", err)
	}
	if reply.LeafID != "a1" {
		t.Fatalf("非用户目标应落在目标自身: %+v", reply)
	}
}

func TestNavigate非法条目ID被拒且不发命令(t *testing.T) {
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmds)
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	for _, bad := range []string{"", "../etc", "a b", strings.Repeat("x", 65)} {
		if _, err := w.Navigate(context.Background(), bad); err == nil {
			t.Fatalf("非法 ID %q 应被拒绝", bad)
		}
	}
	if _, err := os.Stat(cmds); err == nil {
		body, _ := os.ReadFile(cmds)
		if strings.Contains(string(body), "navigate") {
			t.Fatalf("非法 ID 不应触达 pi: %q", string(body))
		}
	}
}

func TestNavigate目标不存在时报错(t *testing.T) {
	dir := t.TempDir()
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if _, err := w.Navigate(context.Background(), "zz"); err == nil {
		t.Fatal("目标不存在时应报错")
	}
}

// 桥内扩展没注册成功时，prompt 对未知斜杠命令的兜底是「当普通消息发给模型」，
// 盲发会把 `/pi-webui-navigate …` 写进会话正文。必须先自查命令已注册。
func TestNavigate扩展未注册时拒绝且不触达prompt(t *testing.T) {
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmds)
	t.Setenv("FAKE_PI_NAV_UNREGISTERED", "1")
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if _, err := w.Navigate(context.Background(), "u1"); err == nil {
		t.Fatal("扩展未注册时必须拒绝，不能把命令当普通消息发给模型")
	}
	if body, rerr := os.ReadFile(cmds); rerr == nil && strings.Contains(string(body), "/pi-webui-navigate") {
		t.Fatalf("扩展未注册时不得把跳转命令发给 pi: %q", string(body))
	}
}

func TestNavigate未启用时报错(t *testing.T) {
	m, cwd := newTestManager(t) // 未配置跳转扩展
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if _, err := w.Navigate(context.Background(), "u1"); err == nil {
		t.Fatal("未启用跳转时应明确报错，而不是静默无效")
	}
}

func TestNavigate运行中拒绝(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_PI_DELAY_MS", "1500")
	t.Setenv("FAKE_PI_DELAY_METHOD", "prompt")
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 起一条会在 pi 侧挂住的 prompt，让 worker 变忙。
	go func() { _ = w.Prompt(context.Background(), "慢请求", "", nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for !w.Info().Busy && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !w.Info().Busy {
		t.Fatal("夹具未进入忙状态")
	}
	if _, err := w.Navigate(context.Background(), "u1"); err == nil {
		t.Fatal("运行中的会话应拒绝跳转")
	}
}

// 忙会话必须能被强制停止（删除会话依赖它）：非强制拒绝，强制成功。
func Test忙会话强制停止(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_PI_DELAY_MS", "3000")
	t.Setenv("FAKE_PI_DELAY_METHOD", "prompt")
	m, cwd := newTestManager(t, navTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	id := w.Info().SessionID
	go func() { _ = w.Prompt(context.Background(), "慢请求", "", nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for !w.Info().Busy && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !w.Info().Busy {
		t.Fatal("夹具未进入忙状态")
	}
	if _, err := m.StopSession(id, false); err == nil {
		t.Fatal("非强制停止忙会话应被拒绝")
	}
	stopped, err := m.StopSession(id, true)
	if err != nil || !stopped {
		t.Fatalf("强制停止失败: stopped=%v err=%v", stopped, err)
	}
	// 表项清理由退出协程完成，与 Stop 返回之间隔着一个调度窗口：轮询等待。
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := m.Get(id); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("强制停止后 worker 仍应在停止流程中被清理")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
