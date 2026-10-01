package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"pi-bridge-go/internal/jsonl"
	"pi-bridge-go/internal/protocol"
)

// errStopWalk 用于提前结束目录遍历，区别于真正的错误。
var errStopWalk = errors.New("stop walk")

func isStopWalk(err error) bool { return errors.Is(err, errStopWalk) }

// SearchLimits 约束全文搜索的规模，避免一次搜索拖垮桥。
// MaxMatchesLimit 是一次搜索允许返回的命中数上限。
// 客户端只能在这个范围内收紧或放宽，不能把默认值顶穿：
// 每条命中都带摘要与元数据，这个数直接决定响应体积（B72）。
const MaxMatchesLimit = 500

// SearchLimits 约束一次搜索的资源占用。
type SearchLimits struct {
	MaxFiles     int
	MaxFileBytes int64
	LineBytes    int
	MaxMatches   int
	MaxSnippet   int
	// MaxTotalBytes 是一次搜索累计读取的字节上限。
	//
	// MaxFileBytes 管单个文件，但它管不住「很多个大文件」：
	// MaxFiles(200) × MaxFileBytes(256 MiB) 的理论最坏是几十 GB。
	// 命中足够时搜索会提前停（MaxMatches），但罕见词必须读完所有候选，
	// 所以需要一道按总量计的兕底。
	MaxTotalBytes int64
	// Cwd 非空时只搜该工作区的会话。
	//
	// 为什么要跟列表筛选一致：用户先筛到某个工作区、再在搜索框输入时，
	// 他会以为搜的就是眼前这一批；返回别的项目的命中会很难理解。
	Cwd string
}

// DefaultSearchLimits 给出默认搜索限额。
//
// MaxFileBytes 为什么从 16 MiB 提到与会话上限（256 MiB）一致：
// 两个限制都在回答「这个文件能不能用」，给出不同答案本身就是缺陷——
// 实测本机 86.9 MB 的会话在列表里能打开、在搜索里却完全搜不到
// （默认限额下命中数为 0，而它就在那里）。同一个文件不该有两种命运。
//
// 代价（实测，真实记录构成的会话）：
//
//	常词（提前达 MaxMatches）  33 ms / 分配 10 MiB
//	罕见词（读完全部候选）    652 ms / 分配 141 MiB（共 130 MB 文件）
//
// 652 ms 对一次用户主动触发的搜索是可接受的（命令超时预算是 1 分钟），
// 而 MaxTotalBytes 把「装满大文件的目录」的最坏情况拦在 512 MiB 以内。
func DefaultSearchLimits() SearchLimits {
	return SearchLimits{
		MaxFiles: 200, MaxFileBytes: 256 << 20, LineBytes: 1 << 20,
		MaxMatches: 100, MaxSnippet: 240, MaxTotalBytes: 512 << 20,
	}
}

// Match 是一条搜索结果。
type Match struct {
	SessionID string `json:"sessionId"`
	EntryID   string `json:"entryId"`
	Title     string `json:"title"`
	Cwd       string `json:"cwd"`
	Role      string `json:"role,omitempty"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp,omitempty"`
}

// SearchResult 是搜索结果与截断标记。
type SearchResult struct {
	Matches   []Match `json:"matches"`
	Scanned   int     `json:"scanned"`
	Truncated bool    `json:"truncated"`
}

// Search 在受管会话目录内做大小写不敏感的子串搜索。
// 设计约束：
//   - 只读，绝不修改会话文件；
//   - 逐文件、逐行扫描，命中数、文件数、单文件体积都有上限；
//   - 达到任一上限立即停止并标记 truncated，不做无界扫描。
func (s *Store) Search(ctx context.Context, query string, limits SearchLimits) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, protocol.E("invalid_params", "搜索词不能为空")
	}
	if len(query) > 200 {
		return SearchResult{}, protocol.E("invalid_params", "搜索词过长")
	}
	needle := strings.ToLower(query)
	// 命中数由搜索能力自己兜底：调用方只应该收紧或放宽，不能无上限（B72）。
	if limits.MaxMatches <= 0 {
		limits.MaxMatches = DefaultSearchLimits().MaxMatches
	}
	if limits.MaxMatches > MaxMatchesLimit {
		limits.MaxMatches = MaxMatchesLimit
	}
	out := SearchResult{Matches: []Match{}}
	scanned := 0
	var totalBytes int64
	// 按工作区筛选时，先向索引要一份该工作区的文件集合。
	// 索引是现成的（列表与历史都靠它），这里只是取它的投影，
	// 不在搜索路径上再解析一遍会话头部。
	var allowed map[string]bool
	if cwd := normalizeCwd(limits.Cwd); cwd != "" {
		paths, err := s.index.PathsForCwd(ctx, cwd)
		if err != nil {
			return SearchResult{}, err
		}
		allowed = paths
	}
	walkErr := walkDir(ctx, s.root, ".", 0, func(path string, size int64, _ time.Time) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if allowed != nil && !allowed[path] {
			return nil
		}
		if len(out.Matches) >= limits.MaxMatches || scanned >= limits.MaxFiles {
			out.Truncated = true
			return errStopWalk
		}
		// 总字节预算：候选文件无论被搜还是被跳过，都先记账。
		// 否则一个装满大文件的目录会先逐个 stat 到 MaxFiles 才停（同 B43 的思路）。
		if limits.MaxTotalBytes > 0 && totalBytes+size > limits.MaxTotalBytes {
			out.Truncated = true
			return errStopWalk
		}
		totalBytes += size
		if size > limits.MaxFileBytes {
			// 跳过的超大文件也占访问预算：否则一个装满大文件的目录
			// 会把每个都 stat 一遍才停（B43）。
			scanned++
			out.Truncated = true
			return nil
		}
		scanned++
		return s.searchFile(ctx, path, needle, limits, &out)
	})
	if walkErr != nil && !isStopWalk(walkErr) {
		return SearchResult{}, walkErr
	}
	out.Scanned = scanned
	if walkErr != nil && isStopWalk(walkErr) {
		out.Truncated = true
	}
	return out, nil
}

// searchFile 在单个会话文件内搜索。
// ctx 逐行检查：单文件搜索以前只在文件之间响应取消，
// 一个大文件就能把取消拖到扫描完（B43）。
func (s *Store) searchFile(ctx context.Context, path, needle string, limits SearchLimits, out *SearchResult) error {
	f, err := s.root.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	// 句柄上的真实尺寸与目录项可能不同：还在增长的文件在这里再判一次，
	// 否则「目录项当时很小」的文件会把整个大文件读进来（B43）。
	if st.Size() > limits.MaxFileBytes {
		out.Truncated = true
		return nil
	}
	r := bufio.NewReader(io.LimitReader(f, st.Size()))
	sessionID := sessionIDFromPath(path)
	start := len(out.Matches)
	var title, firstText, cwd string
	defer func() {
		if title == "" {
			title = firstText
		}
		for i := start; i < len(out.Matches); i++ {
			out.Matches[i].Title = title
			out.Matches[i].Cwd = cwd
		}
	}()
	// 复用缓冲：命中只产出 string（摘要/标题），不保留原始字节。
	var reader jsonl.Reusable
	for i := 0; ; i++ {
		// 每 64 行响应一次取消：扫描成本主要在解析，而不是 I/O。
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		b, _, e := reader.Read(r, limits.LineBytes)
		if e != nil {
			// 末尾半行忽略，那是 Pi 正在追加的正常状态。
			// 超大行必须跳过并标记截断：静默跳过会让后续命中被漏掉，
			// 却向调用方报告「结果完整」（B28）。
			if errors.Is(e, jsonl.ErrTooLarge) {
				out.Truncated = true
				return nil
			}
			return nil
		}
		// 会话 ID 以头部记录为准，文件名只作兜底。
		var header struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Cwd  string `json:"cwd"`
		}
		if json.Unmarshal(b, &header) == nil && header.Type == "session" && ValidID(header.ID) {
			sessionID = header.ID
			cwd = header.Cwd
			continue
		}
		if header.Type == "session_info" {
			var info struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(b, &info) == nil {
				title = shortTitle(info.Name, 160)
			}
			continue
		}
		if firstText == "" && header.Type == "message" && bytes.Contains(b, []byte(`"user"`)) {
			var item struct {
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(b, &item) == nil && item.Message.Role == "user" {
				firstText = shortTitle(flattenContent(item.Message.Content), 80)
			}
		}
		if !bytes.Contains(bytes.ToLower(b), []byte(needle)) {
			continue
		}
		var item struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
			Summary   string          `json:"summary"`
		}
		if json.Unmarshal(b, &item) != nil {
			continue
		}
		role := ""
		text := item.Summary
		if len(item.Message) > 0 {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
				Command string          `json:"command"`
			}
			if json.Unmarshal(item.Message, &msg) == nil {
				role = msg.Role
				text = flattenContent(msg.Content)
				if text == "" {
					text = msg.Command
				}
			}
		}
		out.Matches = append(out.Matches, Match{
			SessionID: sessionID,
			EntryID:   item.ID,
			Role:      role,
			Snippet:   snippet(text, needle, limits.MaxSnippet),
			Timestamp: item.Timestamp,
		})
		if len(out.Matches) >= limits.MaxMatches {
			out.Truncated = true
			return errStopWalk
		}
	}
}

// flattenContent 把字符串或内容块数组压成一段纯文本，用于生成摘要片段。
// 形状由首字节判断，不走「先试字符串、失败再试数组」那条试错路径。
func flattenContent(raw json.RawMessage) string {
	switch shapeOf(raw) {
	case shapeString:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	case shapeArray:
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	default:
		// 空值、null、对象等形状都没有可压的文本。
		return ""
	}
}

// snippet 截取命中位置附近的文本，控制在固定长度内。
func snippet(text, needle string, max int) string {
	if max <= 0 {
		max = 240
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	lower := strings.ToLower(text)
	idx := strings.Index(lower, needle)
	if idx < 0 || idx > len(text) {
		return string(runes[:max]) + "…"
	}
	// 以 rune 为单位回退，避免把 UTF-8 序列切断。
	start := len([]rune(text[:idx]))
	half := max / 2
	if start > half {
		start -= half
	} else {
		start = 0
	}
	end := start + max
	if end > len(runes) {
		end = len(runes)
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// sessionIDFromPath 从文件名解析会话 ID，仅在头部缺失时兜底。
func sessionIDFromPath(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".jsonl")
}
