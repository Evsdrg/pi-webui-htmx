package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pi-bridge-go/internal/magiccontext"
	"pi-bridge-go/internal/presentation"
)

// withMagicContextDB 建一个带假数据的 magic-context 库，并让桥指向它。
// 缺 sqlite3 时跳过——这条测试验证的是「桥把库内容渲染成 HTML」，
// 不是 sqlite 本身。
func withMagicContextDB(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("本机没有 sqlite3")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "context.db")
	schema := `CREATE TABLE memories (id INTEGER PRIMARY KEY, project_path TEXT, category TEXT, content TEXT, importance INTEGER, scope TEXT, shareable INTEGER, source_type TEXT, seen_count INTEGER, retrieval_count INTEGER, updated_at INTEGER, status TEXT);
CREATE TABLE compartments (id INTEGER PRIMARY KEY, session_id TEXT, sequence INTEGER, title TEXT, content TEXT, episode_type TEXT, importance INTEGER, start_message INTEGER, end_message INTEGER, created_at INTEGER, harness TEXT);
CREATE TABLE user_memories (id INTEGER PRIMARY KEY, content TEXT, status TEXT, created_at INTEGER, updated_at INTEGER);
CREATE TABLE notes (id INTEGER PRIMARY KEY, type TEXT, status TEXT, content TEXT, session_id TEXT, project_path TEXT, surface_condition TEXT, created_at INTEGER, updated_at INTEGER);
CREATE TABLE dream_runs (id INTEGER PRIMARY KEY, project_path TEXT, started_at INTEGER, finished_at INTEGER, tasks_succeeded INTEGER, tasks_failed INTEGER, smart_notes_surfaced INTEGER, smart_notes_pending INTEGER, memory_changes_json TEXT);
CREATE TABLE session_meta (session_id TEXT PRIMARY KEY, harness TEXT);
INSERT INTO memories (project_path,category,content,importance,scope,source_type,seen_count,retrieval_count,updated_at,status) VALUES
 ('dir:0123abcd4567','CONSTRAINTS','桥的注释必须用中文',50,'project','user',3,1,1790682817870,'active'),
 ('dir:0123abcd4567','ARCHITECTURE','桥在 /srv/projects/pi/pi-bridge-go',40,'project','dream',1,0,1790655271881,'active'),
 ('dir:0123abcd4567','CONFIG_VALUES','这是一条很长的记忆，用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头用来验证列表视图只渲染截断后的开头。桥侧用 substr 截断，界面再补省略号，全文要显式展开。末尾标记 MUST_NOT_APPEAR 不应出现在列表里。',30,'project','dream',1,0,1790600000000,'active');
INSERT INTO compartments (session_id,sequence,title,content,episode_type,start_message,end_message,created_at,harness) VALUES ('sess-abcdef123456',1,'搭建桥','内容','feature',0,10,1790682817870,'pi');
INSERT INTO user_memories (content,status,created_at,updated_at) VALUES ('用中文写注释','active',1790682817870,1790682817870);
INSERT INTO notes (type,status,content,session_id,project_path,surface_condition,created_at,updated_at) VALUES ('note','active','待跟进','sess-abcdef123456','dir:0123abcd4567','当 PR 合并时',1790682817870,1790682817870);
INSERT INTO dream_runs (project_path,started_at,finished_at,tasks_succeeded,tasks_failed,smart_notes_surfaced,smart_notes_pending) VALUES ('dir:0123abcd4567',1790682817870,1790682900000,3,1,2,0);
INSERT INTO session_meta (session_id,harness) VALUES ('sess-abcdef123456','pi');`
	cmd := exec.Command("sqlite3", db, schema)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("建库失败：%v（%s）", err, out)
	}
	t.Setenv("MAGIC_CONTEXT_STORAGE_DIR", dir)
}

func getFragment(t *testing.T, s *Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 应返回 200，得到 %d：%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestMagicContextPanelRendersMemories(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	body := getFragment(t, s, "/ui/mc?kind=memories")
	for _, want := range []string{"CONSTRAINTS", "ARCHITECTURE", "桥的注释必须用中文", "条记忆"} {
		if !strings.Contains(body, want) {
			t.Errorf("面板应包含 %q", want)
		}
	}
	// 必须显示数据来源，否则用户不知道这数据从哪来。
	if !strings.Contains(body, "本机存储") {
		t.Error("面板应标明数据来源")
	}
}

func TestMagicContextPanelTruncatesContentInList(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	body := getFragment(t, s, "/ui/mc?kind=memories")
	// 长记忆的末尾绝不能出现在列表 HTML 里：列表只给截断后的开头。
	if strings.Contains(body, "MUST_NOT_APPEAR") {
		t.Error("列表不应包含完整正文的末尾")
	}
	// 截断处要显示省略号，否则用户会以为那就是全文。
	if !strings.Contains(body, "…") {
		t.Error("截断处应显示省略号")
	}
	// 但开头必须在。
	if !strings.Contains(body, "这是一条很长的记忆") {
		t.Error("列表应包含截断后的开头")
	}
}

func TestMagicContextPanelCoversEveryKind(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	for _, kind := range []string{"memories", "compartments", "directives", "notes", "dreams"} {
		body := getFragment(t, s, "/ui/mc?kind="+kind)
		if !strings.Contains(body, "mc-panel") {
			t.Errorf("%s 分区应渲染出面板", kind)
		}
		if strings.Contains(body, "这个分区暂时没有内容") {
			t.Errorf("%s 分区有数据却显示空态", kind)
		}
	}
}

func TestMagicContextPanelFallsBackOnUnknownKind(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	// 未知分区回落默认分区，而不是报错——htmx 不交换 4xx/5xx。
	body := getFragment(t, s, "/ui/mc?kind=sqlite_master")
	if !strings.Contains(body, "CONSTRAINTS") {
		t.Error("未知分区应回落默认分区")
	}
}

func TestMagicContextPanelWithoutDatabase(t *testing.T) {
	s := newTestServerWithUI(t)
	// 不设 MAGIC_CONTEXT_STORAGE_DIR：指向一个空目录，模拟没装扩展。
	empty := t.TempDir()
	t.Setenv("MAGIC_CONTEXT_STORAGE_DIR", empty)
	body := getFragment(t, s, "/ui/mc")
	// 库不存在时给一句可读说明，仍然 200——否则界面永远停在占位符。
	if !strings.Contains(body, "未检测到") {
		t.Errorf("应说明未检测到，得到 %s", body)
	}
}

func TestMagicContextPanelIsReadableWithoutSQLiteBinary(t *testing.T) {
	// 把 PATH 清空，模拟本机没有 sqlite3：必须给出可读原因而不是崩掉。
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	t.Setenv("PATH", t.TempDir())
	body := getFragment(t, s, "/ui/mc")
	if !strings.Contains(body, "sqlite3") {
		t.Errorf("应说明缺少 sqlite3，得到 %s", body)
	}
}

func TestMagicContextPanelDoesNotLeakSessionIdFully(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	body := getFragment(t, s, "/ui/mc?kind=compartments")
	// 会话 ID 只显示前 8 位：完整 UUID 没有信息量还占宽度。
	if strings.Contains(body, "sess-abcdef123456") {
		t.Error("会话 ID 不应完整显示")
	}
	if !strings.Contains(body, "sess-abc") {
		t.Errorf("应显示缩写的会话 ID，得到 %s", body)
	}
}

func TestMagicContextPanelListsFilters(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	body := getFragment(t, s, "/ui/mc?kind=memories")
	// 分类与项目筛选必须出现在界面上，否则几百条记忆没法看。
	if !strings.Contains(body, "data-mc-filter=\"category\"") {
		t.Error("应提供分类筛选")
	}
	if !strings.Contains(body, "data-mc-filter=\"project\"") {
		t.Error("应提供项目筛选")
	}
	if !strings.Contains(body, "dir:0123abcd4567") {
		t.Error("应列出项目键")
	}
}

func TestMagicContextPanelPagination(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	body := getFragment(t, s, "/ui/mc?kind=memories&offset=1&limit=1")
	if !strings.Contains(body, "本页 1 条") {
		t.Errorf("应报告本页行数，得到 %s", body)
	}
	_ = os.Getenv("PATH")
}

// newTestServerWithUI 与 newTestServer 相同，但强制加载 UI 包。
// /ui/* 片段依赖模板，没有 UI 包时桥会回 404——那是正确行为，
// 所以测面板必须显式带 UI 包目录。
func newTestServerWithUI(t *testing.T) *Server {
	t.Helper()
	dir := os.Getenv("PI_WEBUI_DIR")
	if dir == "" {
		// 默认指向仓库内的检出；CI 上可用环境变量覆盖。
		dir = "../../pi-webui-htmx"
	}
	rendered, err := presentation.LoadFromDir(dir)
	if err != nil {
		t.Skipf("加载 UI 包失败（设 PI_WEBUI_DIR 指向 pi-webui-htmx 检出）：%v", err)
	}
	server, _, _ := newTestServer(t)
	server.ui = rendered
	server.ui.SetMagicContext(magiccontext.NewStore())
	return server
}

func TestMagicContextPanelAppliesCategoryFilter(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	// 不带筛选：三条 active 记忆。
	all := getFragment(t, s, "/ui/mc?kind=memories")
	if strings.Count(all, "mc-row-title") != 3 {
		t.Errorf("应列出 3 条记忆，得到 %d", strings.Count(all, "mc-row-title"))
	}
	// 带分类筛选：只剩 CONSTRAINTS。
	// 断言走行内容而不是分类名——分类名同时出现在筛选下拉的选项里，
	// 用它判断会把「选项还在」误判成「筛选没生效」。
	filtered := getFragment(t, s, "/ui/mc?kind=memories&category=CONSTRAINTS")
	if !strings.Contains(filtered, "桥的注释必须用中文") {
		t.Error("筛选结果应只剩 CONSTRAINTS 那条")
	}
	if strings.Contains(filtered, "桥在 /srv/projects") {
		t.Error("筛选结果不应包含 ARCHITECTURE 那条")
	}
	if !strings.Contains(filtered, "分区共 1 条") {
		t.Errorf("总数应跟着筛选变成 1，得到 %s", filtered)
	}
}

func TestMagicContextPanelIgnoresInvalidFilter(t *testing.T) {
	s := newTestServerWithUI(t)
	withMagicContextDB(t)
	// 非法分类值被忽略而不是让整页失败：htmx 不交换错误状态码，
	// 报错会让面板永远停在占位符。
	body := getFragment(t, s, "/ui/mc?kind=memories&category=x%27--")
	if !strings.Contains(body, "ARCHITECTURE") {
		t.Error("非法筛选值应被忽略，仍返回全部记忆")
	}
}
