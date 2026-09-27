package protocol

import "testing"

// Test有副作用的方法都要求intent 覆盖 P2 的核心不变量：
// 漏一个就会重现 B30——崩溃后无法判断命令是否执行过。
func Test有副作用的方法都要求intent(t *testing.T) {
	mustIntent := []string{
		"session.prompt", "session.steer", "session.follow_up", "session.bash",
		"session.compact", "session.set_model", "session.set_thinking",
		"session.set_queue_mode", "session.set_auto_compaction", "session.set_auto_retry",
		"session.set_name", "session.cycle_model", "session.cycle_thinking",
		"session.new", "session.switch", "session.fork", "session.clone",
		"session.start", "session.ui_response", "session.export_html",
		"config.models.write", "sessions.delete",
		"terminal.open",
	}
	for _, m := range mustIntent {
		if !NeedsIntent(m) {
			t.Errorf("有副作用的方法 %q 未要求 intent", m)
		}
	}
}

// Test只读方法不写intent 反向约束：给纯读方法写 intent 会白白消耗回执预算。
func Test只读方法不写intent(t *testing.T) {
	reads := []string{
		"worker.list", "session.state", "session.tree", "session.entries",
		"sessions.search", "files.read", "files.index", "git.status", "git.diff",
		"config.models", "config.models.raw", "config.settings", "config.trust",
		"config.catalog", "config.packages", "config.models.discover", "config.models.test",
		"terminal.list",
	}
	for _, m := range reads {
		if NeedsIntent(m) {
			t.Errorf("只读方法 %q 不应要求 intent", m)
		}
	}
}

// Test控制命令可插队 覆盖取消类边界：abort/stop 不能排在长命令后面。
func Test控制命令可插队(t *testing.T) {
	controls := []string{
		"session.abort", "session.stop", "session.abort_retry",
		"session.abort_bash", "terminal.close", "session.ui_response",
	}
	for _, m := range controls {
		if !IsUrgent(m) {
			t.Errorf("控制命令 %q 未标记为可插队", m)
		}
	}
	if IsUrgent("session.prompt") {
		t.Error("普通执行命令不应插队")
	}
}

// Test只有非幂等命令落回执 覆盖资源边界：
// 给轮询类的只读命令写回执会造成持续磁盘 churn。
func Test只有非幂等命令落回执(t *testing.T) {
	no := []string{
		"worker.list", "session.state", "session.tree", "session.entries",
		"sessions.search", "files.read", "files.index", "git.status", "git.diff",
		"terminal.list", "terminal.input", "terminal.resize",
	}
	for _, m := range no {
		if RecordsOutcome(m) {
			t.Errorf("幂等/高频命令 %q 不应落回执", m)
		}
	}
	yes := []string{
		"session.prompt", "session.steer", "session.compact", "session.set_model",
		"session.fork", "session.clone", "session.switch", "session.new",
		"session.start", "session.stop", "session.abort", "session.ui_response",
		"config.models.write", "sessions.delete", "terminal.open", "terminal.close",
		"session.export_html", "config.models.discover", "session.subscribe",
	}
	for _, m := range yes {
		if !RecordsOutcome(m) {
			t.Errorf("有副作用命令 %q 应落回执", m)
		}
	}
}
