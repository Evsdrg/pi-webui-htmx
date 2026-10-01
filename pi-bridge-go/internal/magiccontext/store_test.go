package magiccontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSQLite 让测试在缺 sqlite3 的机器上也能跑：它建一个临时库，
// 只在 sqlite3 可用时才真正执行查询。
func withSQLite(t *testing.T) (*Store, bool) {
	t.Helper()
	if _, err := lookPath("sqlite3"); err != nil {
		t.Skip("跳过：本机没有 sqlite3（读取 Magic Context 的库需要它）")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "context.db")
	// 直接调 sqlite3 建库：测试不引入 sqlite 驱动，只验证我们的读取路径。
	if _, err := runSQLite(db, `CREATE TABLE memories (id INTEGER PRIMARY KEY, project_path TEXT, category TEXT, content TEXT, importance INTEGER, scope TEXT, shareable INTEGER, source_type TEXT, seen_count INTEGER, retrieval_count INTEGER, updated_at INTEGER, status TEXT);
CREATE TABLE compartments (id INTEGER PRIMARY KEY, session_id TEXT, sequence INTEGER, title TEXT, content TEXT, episode_type TEXT, importance INTEGER, start_message INTEGER, end_message INTEGER, created_at INTEGER, harness TEXT);
CREATE TABLE user_memories (id INTEGER PRIMARY KEY, content TEXT, status TEXT, created_at INTEGER, updated_at INTEGER);
CREATE TABLE notes (id INTEGER PRIMARY KEY, type TEXT, status TEXT, content TEXT, session_id TEXT, project_path TEXT, surface_condition TEXT, created_at INTEGER, updated_at INTEGER);
CREATE TABLE dream_runs (id INTEGER PRIMARY KEY, project_path TEXT, started_at INTEGER, finished_at INTEGER, tasks_succeeded INTEGER, tasks_failed INTEGER, smart_notes_surfaced INTEGER, smart_notes_pending INTEGER, memory_changes_json TEXT);
CREATE TABLE session_meta (session_id TEXT PRIMARY KEY, harness TEXT);
INSERT INTO memories (project_path,category,content,importance,scope,source_type,seen_count,retrieval_count,updated_at,status) VALUES
 ('dir:aaaaaaaaaaaa','CONSTRAINTS','第一条：不能用文本框表达 null',50,'project','dream',3,1,1790682817870,'active'),
 ('dir:aaaaaaaaaaaa','ARCHITECTURE','第二条：桥在 /srv/projects/pi/pi-bridge-go',40,'project','user',1,0,1790655271881,'active'),
 ('dir:bbbbbbbbbbbb','NAMING','别的项目的记忆',10,'project','dream',1,0,1790600000000,'active'),
 ('dir:aaaaaaaaaaaa','CONSTRAINTS','已归档的那条',50,'project','dream',1,0,1790500000000,'archived');
INSERT INTO compartments (session_id,sequence,title,content,episode_type,start_message,end_message,created_at,harness) VALUES
 ('sess-1',1,'搭建桥','内容一','feature',0,10,1790682817870,'pi');
INSERT INTO user_memories (content,status,created_at,updated_at) VALUES ('用中文写注释','active',1790682817870,1790682817870);
INSERT INTO notes (type,status,content,session_id,project_path,surface_condition,created_at,updated_at) VALUES ('note','active','待跟进','sess-1','dir:aaaaaaaaaaaa','当 PR 合并时',1790682817870,1790682817870);
INSERT INTO dream_runs (project_path,started_at,finished_at,tasks_succeeded,tasks_failed,smart_notes_surfaced,smart_notes_pending) VALUES ('dir:aaaaaaaaaaaa',1790682817870,1790682900000,3,1,2,0);
INSERT INTO session_meta (session_id,harness) VALUES ('sess-1','pi'),('sess-2','opencode');`); err != nil {
		t.Fatalf("建库失败：%v", err)
	}
	t.Setenv("MAGIC_CONTEXT_STORAGE_DIR", dir)
	store := NewStore()
	store.resolve()
	return store, true
}

func TestStatusReportsCountsAndSource(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	status := store.Status(context.Background())
	if !status.Available {
		t.Fatalf("库应当可用：%s", status.Reason)
	}
	// 已归档的记忆不能算进「记忆」计数。
	if got := status.Counts["memories"]; got != 3 {
		t.Errorf("记忆计数应为 3（排除已归档），得到 %d", got)
	}
	if got := status.Counts["compartments"]; got != 1 {
		t.Errorf("分段计数应为 1，得到 %d", got)
	}
	if got := status.Counts["user_memories"]; got != 1 {
		t.Errorf("用户指令计数应为 1，得到 %d", got)
	}
	if status.Source == "" {
		t.Error("必须报告数据来源目录，否则用户不知道这数据从哪来")
	}
	if got := status.Harness["pi"]; got != 1 {
		t.Errorf("harness 分布应有 pi=1，得到 %d", got)
	}
}

func TestStatusWithoutDatabaseIsReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAGIC_CONTEXT_STORAGE_DIR", dir)
	store := NewStore()
	status := store.Status(context.Background())
	if status.Available {
		t.Fatal("空目录不应报告可用")
	}
	// 原因必须是人能读的句子，不是一个错误码。
	if !strings.Contains(status.Reason, "未检测到") {
		t.Errorf("原因应说明未检测到，得到 %q", status.Reason)
	}
}

func TestRelativeStorageDirIsRejected(t *testing.T) {
	t.Setenv("MAGIC_CONTEXT_STORAGE_DIR", "relative/shared")
	store := NewStore()
	if store.dbPath != "" {
		t.Errorf("相对路径必须被拒绝，得到 %q", store.dbPath)
	}
}

func TestListTruncatesContentAndPaginates(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	rows, total, err := store.List(context.Background(), KindMemories, 0, 2, Filter{})
	if err != nil {
		t.Fatalf("列出记忆失败：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("limit=2 应返回 2 行，得到 %d", len(rows))
	}
	// 第一行是最新的（updated_at 倒序）。
	if got := rows[0].Text("category"); got != "CONSTRAINTS" {
		t.Errorf("首行应为最新那条，得到 %q", got)
	}
	// 预览被截断：列表不能带完整正文。
	preview := rows[0].Text("preview")
	if len([]rune(preview)) > 240 {
		t.Errorf("预览应被截到 240 字符以内，得到 %d", len([]rune(preview)))
	}
	if rows[0].Int("content_len") < len([]rune(preview)) {
		t.Error("完整长度必须大于预览长度，否则界面不会显示省略号")
	}
	// 翻页要能取到剩下的行。
	rows2, _, err := store.List(context.Background(), KindMemories, 2, 2, Filter{})
	if err != nil {
		t.Fatalf("第二页失败：%v", err)
	}
	if len(rows2) != 1 {
		t.Fatalf("第二页应剩 1 行，得到 %d", len(rows2))
	}
	// 总数必须是分区总行数，不是本页行数：否则「加载更多」会在还有数据时消失。
	if total != 3 {
		t.Errorf("分区总数应为 3，得到 %d", total)
	}
}

func TestTotalCountsWholePartition(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	for kind, want := range map[Kind]int{
		KindMemories:     3,
		KindCompartments: 1,
		KindDirectives:   1,
		KindNotes:        1,
		KindDreams:       1,
	} {
		got, err := store.Total(context.Background(), kind)
		if err != nil {
			t.Fatalf("%s 取总数失败：%v", kind, err)
		}
		if got != want {
			t.Errorf("%s 总数应为 %d，得到 %d", kind, want, got)
		}
	}
}

func TestDetailReturnsFullContent(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	content, err := store.Detail(context.Background(), KindMemories, 1)
	if err != nil {
		t.Fatalf("取正文失败：%v", err)
	}
	if content != "第一条：不能用文本框表达 null" {
		t.Errorf("应返回完整正文，得到 %q", content)
	}
}

func TestDetailRejectsUnknownKind(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	// kind 是表名，不接受调用方拼字符串——白名单外一律拒绝。
	if _, err := store.Detail(context.Background(), Kind("sqlite_master"), 1); err == nil {
		t.Fatal("未知分区必须被拒绝")
	}
}

func TestCategoriesAndProjects(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	cats, err := store.Categories(context.Background())
	if err != nil {
		t.Fatalf("取分类失败：%v", err)
	}
	// 三个 active 分类：CONSTRAINTS / ARCHITECTURE / NAMING。
	// 已归档那条 CONSTRAINTS 不能算进来。
	if len(cats) != 3 {
		t.Fatalf("应有 3 个 active 分类，得到 %d", len(cats))
	}
	// 只统计 active，所以 CONSTRAINTS 是 1 而不是 2。
	for _, cat := range cats {
		if cat.Name == "CONSTRAINTS" && cat.Count != 1 {
			t.Errorf("CONSTRAINTS 应只有 1 条 active，得到 %d", cat.Count)
		}
	}
	projects, err := store.Projects(context.Background())
	if err != nil {
		t.Fatalf("取项目失败：%v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("应有 2 个项目，得到 %d", len(projects))
	}
}

func TestLookPathAndRunHelpers(t *testing.T) {
	// 覆盖 lookPath / runSQLite 两个测试辅助本身，避免它们静默失效。
	if _, err := lookPath("sqlite3"); err != nil {
		t.Skip("本机没有 sqlite3")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "t.db")
	if _, err := runSQLite(db, "CREATE TABLE t (a TEXT); INSERT INTO t VALUES ('x');"); err != nil {
		t.Fatalf("runSQLite 失败：%v", err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("库文件应已创建：%v", err)
	}
}

func TestValidateIdentifierRejectsInjection(t *testing.T) {
	// 分类名会代回 SQL，所以只允许大写字母数字下划线。
	if !validateIdentifier("PROJECT_RULES") {
		t.Error("合法分类名被拒绝")
	}
	// 纯大写的 SQL 关键字不算危险：它会被代回到引号里当字面量。
	// 真正要挡住的是引号、分号、空格这类能改变语句结构的东西。
	for _, bad := range []string{"", "lower", "has space", "x'--", "a;b", "a'b", "a=b"} {
		if validateIdentifier(bad) {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func TestValidateProjectKeyAcceptsOnlyKnownShapes(t *testing.T) {
	if !validateProjectKey("dir:0123abcd4567") {
		t.Error("dir: 形态应被接受")
	}
	if !validateProjectKey("git:0123456789abcdef0123456789abcdef01234567") {
		t.Error("git: 形态应被接受")
	}
	for _, bad := range []string{"", "dir:", "dir:xyz", "dir:0123abcd4567'", "git:short", "unknown:abcd", "dir:0123abcd456", "dir:0123abcd4567g"} {
		if validateProjectKey(bad) {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func TestQueryRejectsPlaceholderMismatch(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	// 占位符与参数数量不一致必须明确失败，不能把 ? 留给 sqlite 报语法错误。
	if _, err := store.query(context.Background(), "SELECT 1 WHERE 1 = ? AND 2 = ?", intArg(1)); err == nil {
		t.Fatal("参数数量不匹配应被拒绝")
	}
}

func TestStringArgsAreQuotedNotInjected(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	// 带引号的字符串必须被转义成字面量，不能被解析成 SQL 语法。
	rows, err := store.query(context.Background(), "SELECT 'a''b' AS quoted, ? AS passed", strArg("x' OR '1'='1"))
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应返回 1 行，得到 %d", len(rows))
	}
	if got := rows[0]["passed"]; got != "x' OR '1'='1" {
		t.Errorf("字符串应原样取出，得到 %v", got)
	}
}

func TestListFiltersByCategoryAndProject(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	rows, total, err := store.List(context.Background(), KindMemories, 0, 50, Filter{Category: "CONSTRAINTS"})
	if err != nil {
		t.Fatalf("按分类筛选失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("CONSTRAINTS 应只剩 1 条 active，得到 %d", len(rows))
	}
	// 总数必须跟着筛选变，否则「分区共 N 条」和能翻到的条数不一致。
	if total != 1 {
		t.Errorf("筛选后总数应为 1，得到 %d", total)
	}
	_, total, err = store.List(context.Background(), KindMemories, 0, 50, Filter{Project: "dir:aaaaaaaaaaaa"})
	if err != nil {
		t.Fatalf("按项目筛选失败：%v", err)
	}
	if total != 2 {
		t.Errorf("dir:aaaaaaaaaaaa 应有 2 条 active，得到 %d", total)
	}
	// 两个条件同时生效。
	rows, _, err = store.List(context.Background(), KindMemories, 0, 50, Filter{Category: "CONSTRAINTS", Project: "dir:bbbbbbbbbbbb"})
	if err != nil {
		t.Fatalf("组合筛选失败：%v", err)
	}
	if len(rows) != 0 {
		t.Errorf("dir:bbb 下没有 CONSTRAINTS，应返回 0 行，得到 %d", len(rows))
	}
}

func TestListIgnoresInvalidFilterValues(t *testing.T) {
	store, ok := withSQLite(t)
	if !ok {
		return
	}
	// 非法值被忽略而不是报错：界面上下拉选项不该出现脏数据，
	// 出现了也只可能是有人手改 URL，安静忽略比整页失败好。
	for _, bad := range []Filter{{Category: "x'--"}, {Project: "dir:zzz"}, {Category: "lower"}, {Project: "dir:short"}} {
		rows, _, err := store.List(context.Background(), KindMemories, 0, 50, bad)
		if err != nil {
			t.Fatalf("非法筛选值不应报错：%v", err)
		}
		if len(rows) != 3 {
			t.Errorf("非法筛选值应被忽略（返回全部 3 条），得到 %d", len(rows))
		}
	}
}
