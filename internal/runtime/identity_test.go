package runtime

import (
	"context"
	"encoding/json"
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
	// 身份变化后 seq 归零，旧游标大于新序号时不应谎报可补。
	if items, ok := w.Replay(epoch, 999); !ok || len(items) != 0 {
		t.Fatalf("身份变更后旧游标应无效: %v %v", items, ok)
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
