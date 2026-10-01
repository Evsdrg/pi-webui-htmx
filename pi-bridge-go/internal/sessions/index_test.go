package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeNested 按 Pi 真实布局写入 <cwd 编码>/<时间>_<id>.jsonl。
func writeNested(t *testing.T, sessionDir, cwd, id string, when time.Time) {
	t.Helper()
	// Pi 把 cwd 的 / 换成 -，因此子目录名不含斜杠。
	encoded := replaceAll("--"+filepath.ToSlash(filepath.Clean(cwd))+"--", "/", "-")
	if strings.Contains(encoded, "/") {
		t.Fatalf("编码后的目录名不应包含斜杠: %s", encoded)
	}
	dir := filepath.Join(sessionDir, encoded)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := when.UTC().Format("2006-01-02T15-04-05.000Z") + "_" + id + ".jsonl"
	body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"` + when.UTC().Format("2006-01-02T15:04:05.000Z") + `","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func replaceAll(s, old, new string) string {
	out := ""
	for i := 0; i < len(s); i++ {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out += new
			i += len(old) - 1
			continue
		}
		out += string(s[i])
	}
	return out
}

func TestIndex识别嵌套目录与顺序(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeNested(t, sessionDir, cwd, "old", base)
	writeNested(t, sessionDir, cwd, "new", base.Add(time.Hour))
	ctx := context.Background()
	list, err := store.List(ctx, 0, 10)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != "new" || list.Items[1].ID != "old" {
		t.Fatalf("顺序或数量异常: %+v", list.Items)
	}
	if list.Truncated {
		t.Fatal("未超限不应标记截断")
	}
	// 嵌套目录下的历史仍可读。
	page, err := store.History(ctx, "old", "", "", 10)
	if err != nil {
		t.Fatalf("嵌套目录历史读取失败: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("历史条目数量异常: %+v", page)
	}
}

func TestIndex缓存命中不做磁盘遍历(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "s1", time.Now())
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	stats := store.Index().Stats()
	if stats["sessions"].(int) != 1 {
		t.Fatalf("索引规模异常: %v", stats)
	}
	// 直接改文件系统但不改目录 mtime 的情况：文件内容变化由 TTL 兜底。
	writeNested(t, sessionDir, cwd, "s2", time.Now().Add(time.Minute))
	if _, err := store.List(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	stats = store.Index().Stats()
	if stats["sessions"].(int) != 2 {
		t.Fatalf("新增文件后索引未刷新: %v", stats)
	}
}

func TestIndex超限时标记截断且不隐藏最新项(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t, cwd)
	limits := DefaultLimits()
	limits.Files = 3
	store, err := New(sessionDir, policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		writeNested(t, sessionDir, cwd, idFromIndex(i), base.Add(time.Duration(i)*time.Hour))
	}
	list, err := store.List(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("应受上限约束: %d", len(list.Items))
	}
	if !list.Truncated {
		t.Fatal("超过上限必须标记截断")
	}
	if list.Items[0].ID != idFromIndex(5) {
		t.Fatalf("应保留最新的会话: %s", list.Items[0].ID)
	}
}

func idFromIndex(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "s0"
	}
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	return "s" + out
}

// Test索引TTL内不做全树遍历 覆盖 B29：
// 旧实现在 TTL 内仍遍历整棵树计算指纹。判定方式是把指纹值破坏掉——
// 若 fresh() 真的在算指纹，它必然失配并触发重建；改用轻量戳则不受影响。
func Test索引TTL内不做全树遍历(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	for i := 0; i < 120; i++ {
		writeNested(t, sessionDir, cwd, "sess-"+strconv.Itoa(i), time.Now().Add(time.Duration(i)*time.Second))
	}
	ctx := context.Background()
	if _, err := store.List(ctx, 0, 200); err != nil {
		t.Fatal(err)
	}
	// 破坏指纹：只有「TTL 内仍在算指纹」的实现会被它影响。
	store.index.mu.Lock()
	store.index.fingerprint = "已被破坏的指纹"
	builtBefore := store.index.builtAt
	store.index.mu.Unlock()

	list, err := store.List(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 120 {
		t.Fatalf("二次列表条数异常: %d", len(list.Items))
	}
	store.index.mu.RLock()
	builtAfter := store.index.builtAt
	store.index.mu.RUnlock()
	if !builtAfter.Equal(builtBefore) {
		t.Fatal("TTL 内的列表触发了索引重建，说明仍在做全树指纹比较")
	}
}

// Test指纹计算可被取消 覆盖 B52：
// computeFingerprint 必须接收 context，否则取消无法中断全树扫描。
func Test指纹计算可被取消(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	for i := 0; i < 30; i++ {
		writeNested(t, sessionDir, cwd, "sess-"+strconv.Itoa(i), time.Now().Add(time.Duration(i)*time.Second))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.index.computeFingerprint(ctx); err == nil {
		t.Fatal("已取消的上下文应让指纹计算失败")
	}
}

// Test空目录不受文件上限约束 覆盖 B52 的另一半：
// 海量空目录不应被当成文件计数，但遍历深度仍受限制。
func Test空目录不受文件上限约束(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	writeNested(t, sessionDir, cwd, "only", time.Now())
	// 造一批空目录：数量远超会话文件上限。
	base := filepath.Join(sessionDir, "empty")
	for i := 0; i < 40; i++ {
		if err := os.MkdirAll(filepath.Join(base, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	list, err := store.List(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("空目录不应被当成会话: %+v", list.Items)
	}
}
