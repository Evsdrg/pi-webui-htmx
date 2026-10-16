// 配置重载（写 models.json 后让当前 worker 重新读取）的桥侧通道。
//
// 背景：Pi 的 ModelRuntime 在进程创建时只读一次 models.json，之后新增的
// provider/模型不在内存快照里——get_available_models 与 set_model 都读那份
// 快照，于是「刚添加的模型」会报 Model not found，直到工作进程重启。
// RPC 命令表没有刷新模型的命令，唯一通道是扩展命令上下文里的 reload：
// 与跳转扩展同一套机制，经内部命令触发、结果经结果文件回传。
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reloadTestConfig 给夹具打开重载通道：扩展路径只用于「已启用」判定，
// fake-pi 不加载它；结果目录用来接收 fake-pi 写回的结果文件。
func reloadTestConfig(t *testing.T, resultDir string) func(*Config) {
	return func(c *Config) {
		c.ReloadExt = filepath.Join(t.TempDir(), "bridge-reload.mjs")
		c.ReloadResultDir = resultDir
	}
}

func TestReload让工作进程重新读取配置(t *testing.T) {
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmds)
	m, cwd := newTestManager(t, reloadTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := w.Reload(context.Background()); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	body, rerr := os.ReadFile(cmds)
	if rerr != nil || !strings.Contains(string(body), `"/pi-webui-reload"`) {
		t.Fatalf("pi 未收到重载命令: %v %q", rerr, string(body))
	}
}

// 忙会话必须拒绝重载：reload 会重启扩展与资源，绝不能打断正在跑的回合。
func TestReload在忙会话上被拒绝(t *testing.T) {
	dir := t.TempDir()
	m, cwd := newTestManager(t, reloadTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	w.event([]byte(`{"type":"agent_start"}`))
	if err := w.Reload(context.Background()); err == nil {
		t.Fatal("忙会话上重载应被拒绝")
	}
}

// 扩展未注册时不得盲发命令：prompt 对未知斜杠命令的兜底是「当普通消息
// 发给模型」，盲发会把命令文本写进会话正文。
func TestReload在扩展未注册时明确报错(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_PI_RELOAD_UNREGISTERED", "1")
	m, cwd := newTestManager(t, reloadTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := w.Reload(context.Background()); err == nil {
		t.Fatal("扩展未注册时应报错，而不是把命令当普通消息发出去")
	}
}

// ReloadIdle 只重载空闲 worker：忙 worker 跳过（重载会打断正在跑的回合），
// 空闲 worker 必须真正收到重载命令。这是「写配置后新模型立刻可用」的核心。
func TestReloadIdle只重载空闲进程(t *testing.T) {
	dir := t.TempDir()
	cmds := filepath.Join(dir, "cmds.jsonl")
	t.Setenv("FAKE_PI_CMDS_FILE", cmds)
	m, cwd := newTestManager(t, reloadTestConfig(t, dir))
	w, err := m.Start(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}

	// 忙时跳过：不得向忙 worker 发送重载命令。
	w.event([]byte(`{"type":"agent_start"}`))
	if ok, failed := m.ReloadIdle(context.Background()); ok != 0 || failed != 0 {
		t.Fatalf("忙 worker 应被跳过，实际 ok=%d failed=%d", ok, failed)
	}
	if body, _ := os.ReadFile(cmds); strings.Contains(string(body), `"/pi-webui-reload"`) {
		t.Fatalf("忙 worker 不应收到重载命令: %q", string(body))
	}

	// 空闲时重载：必须真正下发命令。
	w.event([]byte(`{"type":"agent_settled"}`))
	ok, failed := m.ReloadIdle(context.Background())
	if ok != 1 || failed != 0 {
		t.Fatalf("应重载 1 个空闲进程，实际 ok=%d failed=%d", ok, failed)
	}
	if w.Info().Busy {
		t.Fatal("重载不应让空闲进程变忙")
	}
	body, _ := os.ReadFile(cmds)
	if !strings.Contains(string(body), `"/pi-webui-reload"`) {
		t.Fatalf("空闲进程未收到重载命令: %q", string(body))
	}
}
