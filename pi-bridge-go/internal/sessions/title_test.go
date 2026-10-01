package sessions

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// countingReaderAt 统计从文件里实际读了多少字节。
// B37 的核心断言：标题只读文件两端，不扫全文。
type countingReaderAt struct {
	r io.ReaderAt
	n int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n += int64(n)
	return n, err
}

// B37：老实现为每条会话从文件头扫到尾找 session_info。
// 现在头部取第一条用户文本、尾部反向找最新重命名，总量远小于文件大小。
func Test标题读取只碰文件两端(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	var body strings.Builder
	body.WriteString(`{"type":"session","version":3,"id":"big","cwd":"/tmp"}` + "\n")
	body.WriteString(`{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"第一条提问"}}` + "\n")
	filler := `{"type":"message","id":"f","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 400) + `"}]}}` + "\n"
	for body.Len() < 8<<20 {
		body.WriteString(filler)
	}
	body.WriteString(`{"type":"session_info","id":"n1","name":"尾部重命名"}` + "\n")
	if err := os.WriteFile(path, []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	counter := &countingReaderAt{r: f}
	if name, ok := lastSessionInfoName(counter, info.Size()); !ok || name != "尾部重命名" {
		t.Fatalf("尾部重命名未取到: %q %v", name, ok)
	}
	if text, ok := firstUserText(counter, info.Size()); !ok || text != "第一条提问" {
		t.Fatalf("首条用户文本未取到: %q %v", text, ok)
	}
	// 8 MiB 文件：两端读取加起来应远小于 1 MiB。
	if counter.n > 1<<20 {
		t.Fatalf("标题读取应只碰两端（旧实现扫全文），实际读了 %d 字节", counter.n)
	}
}

// 跨块边界：session_info 恰好被尾部窗口切开时也必须完整读到。
// 相邻块有重叠，所以这种行在某一轮里是完整的。
func Test标题跨块边界仍取最新重命名(t *testing.T) {
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	head := `{"type":"session","version":3,"id":"edge","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"提问"}}` + "\n"
	infoLine := `{"type":"session_info","id":"n1","name":"跨块标题"}` + "\n"
	filler := `{"type":"message","id":"f","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", 400) + `"}]}}` + "\n"
	padLine := func(n int) string {
		return `{"type":"message","id":"pad","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("p", n) + `"}]}}` + "\n"
	}
	// 八种偏移：让 session_info 落在尾部窗口边界的相邻位置，
	// 至少有一种会把这一行切开。
	for pad := 0; pad < 8; pad++ {
		id := "edge" + strconv.Itoa(pad)
		var body strings.Builder
		body.WriteString(strings.Replace(head, `"id":"edge"`, `"id":"`+id+`"`, 1))
		body.WriteString(infoLine)
		fill := titleTailChunk + 1 - len(infoLine) + pad
		for i := 0; i < fill/len(filler); i++ {
			body.WriteString(filler)
		}
		if rest := fill % len(filler); rest > 0 {
			body.WriteString(padLine(rest - 1))
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, _, err := store.index.Page(context.Background(), 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("应有 8 条会话，实际 %d", len(rows))
	}
	for _, row := range rows {
		if row.name != "跨块标题" {
			t.Fatalf("%s 的标题跨越窗口边界后丢失: %q", row.id, row.name)
		}
	}
}
