package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFork后进程表重绑定(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	old := w.Info().SessionID
	out, err := w.Fork(ctx, "entry-1")
	if err != nil {
		t.Fatalf("fork 失败: %v", err)
	}
	newID, _ := out["sessionId"].(string)
	if newID == "" || newID == old {
		t.Fatalf("fork 后会话身份应变化: %q -> %q", old, newID)
	}
	// 旧 ID 必须查不到，新 ID 必须指向同一进程。
	if _, err := m.Get(old); err == nil {
		t.Fatal("旧会话 ID 仍残留工作进程")
	}
	got, err := m.Get(newID)
	if err != nil {
		t.Fatalf("新会话 ID 查不到工作进程: %v", err)
	}
	if got != w {
		t.Fatal("新会话 ID 应指向同一工作进程")
	}
	if len(m.List()) != 1 {
		t.Fatalf("进程表应只有一项: %+v", m.List())
	}
	// 身份变化后补发环序号必须重置，旧游标立即失效。
	if _, ok := w.Replay(w.Info().Epoch, 0); !ok {
		t.Fatal("重置后从头补发应可用")
	}
}

func TestClone后重绑定(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	old := w.Info().SessionID
	id, err := w.Clone(ctx)
	if err != nil {
		t.Fatalf("clone 失败: %v", err)
	}
	if id == "" || id == old {
		t.Fatalf("clone 后会话身份应变化: %q -> %q", old, id)
	}
	if _, err := m.Get(id); err != nil {
		t.Fatalf("clone 后查不到新会话: %v", err)
	}
}

func Test切换会话拒绝越界路径(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "relative/path.jsonl", "/etc/passwd", "/tmp/不在受管目录.jsonl"} {
		if _, err := w.SwitchSession(ctx, bad); err == nil {
			t.Fatalf("越界路径 %q 应被拒绝", bad)
		}
	}
	// 已启动进程不应被破坏。
	if _, err := w.State(ctx); err != nil {
		t.Fatalf("拒绝后工作进程应仍可用: %v", err)
	}
}

func Test模型与思考等级校验(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	models, err := w.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0]["id"] != "m1" {
		t.Fatalf("模型列表异常: %v", models)
	}
	// 投影中不得出现供应商私有字段。
	raw, _ := json.Marshal(models[0])
	for _, forbidden := range []string{"headers", "options", "apiKey", "baseUrl"} {
		if contains(string(raw), forbidden) {
			t.Fatalf("模型投影泄露了 %s: %s", forbidden, raw)
		}
	}
	if _, err := w.SetModel(ctx, "", "m1"); err == nil {
		t.Fatal("空 provider 应被拒绝")
	}
	levels, err := w.ThinkingLevels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 3 {
		t.Fatalf("思考等级异常: %v", levels)
	}
	if err := w.SetThinkingLevel(ctx, "不存在的等级"); err == nil {
		t.Fatal("不支持的思考等级应被拒绝")
	}
	if err := w.SetThinkingLevel(ctx, "high"); err != nil {
		t.Fatalf("合法等级应被接受: %v", err)
	}
}

func Test压缩与统计(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Compact(ctx, "保留代码变更")
	if err != nil {
		t.Fatal(err)
	}
	if out["summary"] != "压缩摘要" || out["tokensBefore"].(int) != 100 {
		t.Fatalf("压缩结果异常: %v", out)
	}
	stats, err := w.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["totalMessages"].(float64) != 2 {
		t.Fatalf("统计异常: %v", stats)
	}
}

func Test队列模式参数校验(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetQueueMode(ctx, "steering", "非法值"); err == nil {
		t.Fatal("非法投递模式应被拒绝")
	}
	if err := w.SetQueueMode(ctx, "不存在的kind", "all"); err == nil {
		t.Fatal("非法 kind 应被拒绝")
	}
	if err := w.SetQueueMode(ctx, "followUp", "all"); err != nil {
		t.Fatalf("合法参数应被接受: %v", err)
	}
}

func TestBash输出文件仅限Pi临时文件(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.ReadBashOutput(ctx, "/etc/passwd", 1024); err == nil {
		t.Fatal("任意文件读取必须被拒绝")
	}
	if _, _, err := w.ReadBashOutput(ctx, "", 1024); err == nil {
		t.Fatal("空路径必须被拒绝")
	}
	if _, _, err := w.ReadBashOutput(ctx, "/tmp/pi-bash-x.log", 0); err == nil {
		t.Fatal("非法 maxBytes 必须被拒绝")
	}
	result, err := w.Bash(ctx, "req-bash", "echo hi", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil {
		t.Fatalf("bash 结果缺少退出码: %+v", result)
	}
}

func Test并发身份变更不产生双键(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	// 连续变更身份，进程表必须始终自洽。
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		out, err := w.Fork(ctx, "entry-x")
		if err != nil {
			t.Fatalf("第 %d 次 fork 失败: %v", i, err)
		}
		id, _ := out["sessionId"].(string)
		if seen[id] {
			t.Fatalf("会话 ID 重复出现在进程表: %s", id)
		}
		seen[id] = true
	}
	if len(m.List()) != 1 {
		t.Fatalf("进程表应只有一项: %+v", m.List())
	}
}

func TestReplay环在身份变更后失效(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	_ = w.Prompt(ctx, "hi", "", nil)
	// 等到至少一条事件入环。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Info().Seq > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	epoch := w.Info().Epoch
	if _, ok := w.Replay(epoch, 0); !ok {
		t.Fatal("同 epoch 应可补发")
	}
	if _, err := w.Fork(ctx, "e"); err != nil {
		t.Fatal(err)
	}
	// 身份变更必须换 epoch：旧 epoch 一律拒绝，让调用方重新同步。
	// 只把 seq 归零不够——旧 epoch 配旧 seq 仍会被接受，
	// 客户端会读到新会话的事件却以为还在旧游标上（B65）。
	if items, ok := w.Replay(epoch, 0); ok {
		t.Fatalf("身份变更后旧 epoch 应失效: items=%d ok=%v", len(items), ok)
	}
	if items, ok := w.Replay(epoch, 999); ok {
		t.Fatalf("身份变更后旧 epoch 的任何游标都应失效: items=%d", len(items))
	}
	fresh := w.Info().Epoch
	if fresh == epoch {
		t.Fatal("身份变更后 epoch 未更换")
	}
	if _, ok := w.Replay(fresh, 0); !ok {
		t.Fatal("新 epoch 应可补发")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Test切换到已占用会话被提前拒绝 覆盖 B64：
// Rebind 的冲突检查以前发生在 Pi 切换之后，那时 Pi 已经改了写入目标，
// 旧键下的 worker 仍可 Prompt，形成双写。守卫必须在切换之前就能拒绝。
func Test切换到已占用会话被提前拒绝(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	target := w.Info().SessionID

	// 让目标 ID 被另一个 worker 占用（fixture 只会发固定身份，直接占用表项）。
	other := &Worker{id: target + "-occupant", cfg: m.cfg, done: make(chan struct{})}
	m.mu.Lock()
	m.workers[target] = other
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.workers[target] == other {
			delete(m.workers, target)
		}
		m.mu.Unlock()
	}()

	if err := m.CheckRebindTarget(w, target); err == nil {
		t.Fatal("切换到已占用会话应被拒绝")
	}
	// 拒绝之后 w 的身份不能被动过，占用方也不能被顶掉。
	if w.Info().SessionID != target {
		t.Fatal("守卫拒绝后发起方身份被改动")
	}
	if got := m.workers[target]; got != other {
		t.Fatal("守卫拒绝后占用方被替换")
	}
	// 对自己当前身份的重复绑定不算冲突。
	if err := m.CheckRebindTarget(other, target); err != nil {
		t.Fatalf("同 worker 的当前身份不应冲突: %v", err)
	}
}

// Test同worker重复绑定目标不冲突 覆盖反向边界：
// worker 重绑定到自己当前身份时不能误报冲突。
func Test同worker重复绑定目标不冲突(t *testing.T) {
	m, cwd := newTestManager(t)
	ctx := context.Background()
	w, err := m.Start(ctx, "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckRebindTarget(w, w.Info().SessionID); err != nil {
		t.Fatalf("同 worker 的当前身份不应冲突: %v", err)
	}
}

// writeTestSession 在管理器的会话目录里写一个最小会话文件，供按 ID 恢复使用。
func writeTestSession(t *testing.T, m *Manager, cwd, id string) {
	t.Helper()
	if err := os.MkdirAll(m.cfg.Store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(m.cfg.Store.Dir(), id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
