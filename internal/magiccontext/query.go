package magiccontext

import (
	"context"
	"fmt"
	"strings"
)

// Kind 是面板上的一个分区。
type Kind string

const (
	KindMemories     Kind = "memories"
	KindCompartments Kind = "compartments"
	KindDirectives   Kind = "directives"
	KindNotes        Kind = "notes"
	KindDreams       Kind = "dreams"
)

// Kinds 是全部分区，顺序即界面上的顺序。
var Kinds = []struct {
	Key   Kind
	Label string
	Hint  string
}{
	{KindMemories, "记忆", "跨会话保留下来的项目知识，按分类归组"},
	{KindCompartments, "会话分段", "每个会话被切成的摘要段，按时间倒序"},
	{KindDirectives, "用户指令", "用户明确要求过、需要长期遵守的事"},
	{KindNotes, "笔记", "被搁置的决策与待跟进事项"},
	{KindDreams, "Dreamer", "夜间整理任务的运行记录"},
}

// 各分区的列表查询。全部写死，末尾统一是 LIMIT ? OFFSET ?，
// 由 List 代入 offset 与 limit。
//
// 正文一律截断后返回（substr），完整正文单独给 FullContent 用：
// 一次取回几百行完整记忆会把片段响应撑到几百 KB，而列表视图只需要开头。
var listQueries = map[Kind]string{
	KindMemories: `SELECT id, category, scope, importance, shareable,
		substr(content, 1, 240) AS preview, LENGTH(content) AS content_len,
		source_type, seen_count, retrieval_count, updated_at
		FROM memories WHERE status='active' ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
	KindCompartments: `SELECT id, session_id, sequence, title, episode_type, importance,
		substr(content, 1, 240) AS preview, LENGTH(content) AS content_len,
		start_message, end_message, harness, created_at
		FROM compartments ORDER BY created_at DESC LIMIT ? OFFSET ?`,
	KindDirectives: `SELECT id, substr(content, 1, 240) AS preview, LENGTH(content) AS content_len,
		status, created_at, updated_at FROM user_memories ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
	KindNotes: `SELECT id, type, status, substr(content, 1, 240) AS preview, LENGTH(content) AS content_len,
		session_id, project_path, surface_condition, created_at, updated_at
		FROM notes ORDER BY updated_at DESC LIMIT ? OFFSET ?`,
	KindDreams: `SELECT id, project_path, started_at, finished_at, tasks_succeeded, tasks_failed,
		smart_notes_surfaced, smart_notes_pending, memory_changes_json
		FROM dream_runs ORDER BY started_at DESC LIMIT ? OFFSET ?`,
}

// Row 是列表里的一行。字段按各分区查询的列名取，缺失即为零值。
type Row map[string]any

// ID 返回行主键的字符串形式。
func (r Row) ID() string { return text(r["id"]) }

// Text 取一个字符串字段。
func (r Row) Text(key string) string { return text(r[key]) }

// Int 取一个整数字段。
func (r Row) Int(key string) int { return int(number(r[key])) }

// countQueries 与 listQueries 一一对应，给出该分区的总行数。
// 界面要区分「这一页没有数据」与「这个分区没有数据」，所以总数必须单独查。
var countQueries = map[Kind]string{
	KindMemories:     `SELECT COUNT(*) AS n FROM memories WHERE status='active'`,
	KindCompartments: `SELECT COUNT(*) AS n FROM compartments`,
	KindDirectives:   `SELECT COUNT(*) AS n FROM user_memories`,
	KindNotes:        `SELECT COUNT(*) AS n FROM notes`,
	KindDreams:       `SELECT COUNT(*) AS n FROM dream_runs`,
}

// Total 返回某一分区的总行数。
func (s *Store) Total(ctx context.Context, kind Kind) (int, error) {
	return s.total(ctx, kind, Filter{})
}

// total 是 Total 的带筛选版本：总数必须与列表用同一套条件，
// 否则「分区共 N 条」会和实际能翻到的条数不一致。
func (s *Store) total(ctx context.Context, kind Kind, filter Filter) (int, error) {
	s.resolve()
	if s.dbPath == "" {
		return 0, ErrUnavailable
	}
	query, ok := countQueries[kind]
	if !ok {
		return 0, fmt.Errorf("未知分区 %q", kind)
	}
	query, args := filter.apply(query)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return int(number(rows[0]["n"])), nil
}

// Filter 是记忆分区的筛选条件。只有记忆分区有这两列，
// 其它分区传零值即可。
type Filter struct {
	Category string
	Project  string
}

// apply 把筛选条件拼进语句，并返回要代入的参数。
//
// 两个值都必须先过校验：分类名只允许大写字母数字下划线，
// 项目键只允许 dir:<12 位十六进制> 或 git:<40 位十六进制>。
// 校验不过就忽略这个条件而不是报错——界面上它们是下拉选项，
// 出现非法值只可能是有人手工改 URL，安静忽略比整页失败好。
func (f Filter) apply(query string) (string, []arg) {
	var conditions []string
	var args []arg
	if f.Category != "" && validateIdentifier(f.Category) {
		conditions = append(conditions, "category = ?")
		args = append(args, strArg(f.Category))
	}
	if f.Project != "" && validateProjectKey(f.Project) {
		conditions = append(conditions, "project_path = ?")
		args = append(args, strArg(f.Project))
	}
	if len(conditions) == 0 {
		return query, nil
	}
	// 只替换第一次出现的 WHERE 子句。曾经这里对每个条件各替换一次
	// "WHERE status='active'"，第一个条件改完那段文本就没了，
	// 第二个条件于是替换不到、参数却照样追加，语句与参数对不上。
	return strings.Replace(query, "WHERE status='active'",
		"WHERE status='active' AND "+strings.Join(conditions, " AND "), 1), args
}

// List 返回某一分区的一页，以及该分区的总行数。
//
// 总数不是「本页返回了几行」——那会让界面把第一页的 50 条当成分区全部内容，
// 「加载更多」按钮于是在还有几百条时就消失。
func (s *Store) List(ctx context.Context, kind Kind, offset, limit int, filter Filter) ([]Row, int, error) {
	s.resolve()
	if s.dbPath == "" {
		return nil, 0, ErrUnavailable
	}
	query, ok := listQueries[kind]
	if !ok {
		return nil, 0, fmt.Errorf("未知分区 %q", kind)
	}
	if limit <= 0 || limit > maxRows {
		limit = maxRows
	}
	if offset < 0 {
		offset = 0
	}
	query, filterArgs := filter.apply(query)
	// 注意顺序：语句末尾是 LIMIT ? OFFSET ?，所以先 limit 后 offset。
	args := append(filterArgs, intArg(limit), intArg(offset))
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.total(ctx, kind, filter)
	if err != nil {
		// 总数拿不到不该让整页失败：退回本页行数，界面仍可用。
		total = len(rows)
	}
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		out = append(out, Row(row))
	}
	return out, total, nil
}

// Detail 取某一行的完整正文。
//
// 表名只接受 Kinds 里声明过的那几个：kind 由界面上的分区决定，
// 不是用户输入，但仍然要白名单校验——否则这就是一条 SQL 注入通道。
func (s *Store) Detail(ctx context.Context, kind Kind, id int64) (string, error) {
	s.resolve()
	if s.dbPath == "" {
		return "", ErrUnavailable
	}
	known := false
	for _, entry := range Kinds {
		if entry.Key == kind {
			known = true
			break
		}
	}
	if !known {
		return "", fmt.Errorf("未知分区 %q", kind)
	}
	rows, err := s.query(ctx, fmt.Sprintf("SELECT content FROM %s WHERE id = ?", string(kind)), intArg(int(id)))
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return text(rows[0]["content"]), nil
}

// Categories 返回记忆的分类及各自行数，用于分区内的筛选。
func (s *Store) Categories(ctx context.Context) ([]CategoryCount, error) {
	s.resolve()
	if s.dbPath == "" {
		return nil, ErrUnavailable
	}
	rows, err := s.query(ctx, `SELECT category, COUNT(*) AS n FROM memories
		WHERE status='active' GROUP BY category ORDER BY n DESC`)
	if err != nil {
		return nil, err
	}
	out := make([]CategoryCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, CategoryCount{Name: text(row["category"]), Count: int(number(row["n"]))})
	}
	return out, nil
}

// CategoryCount 是一个分类及其行数。
type CategoryCount struct {
	Name  string
	Count int
}

// ProjectCount 是库里的一个项目及其记忆数。project_path 形如
// "dir:0123abcd4567"（按目录哈希）或 "git:<sha>"（按仓库）。
type ProjectCount struct {
	Key   string
	Count int
}

// Projects 返回库里的项目分布。这个库是跨 harness 共用的，所以会列出
// OpenCode 与 Pi 各自的项目。
func (s *Store) Projects(ctx context.Context) ([]ProjectCount, error) {
	s.resolve()
	if s.dbPath == "" {
		return nil, ErrUnavailable
	}
	rows, err := s.query(ctx, `SELECT project_path, COUNT(*) AS n FROM memories
		WHERE status='active' AND project_path IS NOT NULL
		GROUP BY project_path ORDER BY n DESC`)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, ProjectCount{Key: text(row["project_path"]), Count: int(number(row["n"]))})
	}
	return out, nil
}
