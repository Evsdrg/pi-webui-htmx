package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// B45：导出是平铺投影，不递归——15000 层线性链既不栈溢出也不丢条目。
// Pi Web 的导出把树摊平脚本改成迭代实现正是为了这个形状；
// 桥的导出换成自己的投影后，浏览器侧不再执行任何递归脚本。
func Test导出深链不递归(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := "deep"
	var body strings.Builder
	body.WriteString(`{"type":"session","version":3,"id":"` + id + `","cwd":"` + cwd + `"}` + "\n")
	parent := "null"
	for i := 0; i < 15000; i++ {
		eid := "e" + strconv.Itoa(i)
		body.WriteString(`{"type":"message","id":"` + eid + `","parentId":` + parent + `,"message":{"role":"assistant","content":"第 ` + strconv.Itoa(i) + ` 条"}}` + "\n")
		parent = `"` + eid + `"`
	}
	if err := os.WriteFile(filepath.Join(sessionDir, id+".jsonl"), []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	doc, err := store.ExportDocument(context.Background(), id, DefaultExportLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Rows) != 15000 {
		t.Fatalf("条目应全部导出: %d", len(doc.Rows))
	}
	if doc.Truncated {
		t.Fatal("15000 条在默认上限内，不该标记截断")
	}
	if doc.Leaf != "e14999" {
		t.Fatalf("叶子应是最后一条: %q", doc.Leaf)
	}
}

// 导出对单行超长内容按上限截断，不把整个文件复制进文档。
func Test导出按行截断超长内容(t *testing.T) {
	cwd := t.TempDir()
	store, sessionDir := newStore(t, cwd)
	id := "long"
	big := strings.Repeat("x", 40<<10)
	body := `{"type":"session","version":3,"id":"` + id + `","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"` + big + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, id+".jsonl"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	limits := DefaultExportLimits()
	limits.MaxTextBytes = 1024
	doc, err := store.ExportDocument(context.Background(), id, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Rows) != 1 {
		t.Fatalf("应有一行: %+v", doc.Rows)
	}
	if len(doc.Rows[0].Text) > 1024+64 || !strings.HasSuffix(doc.Rows[0].Text, "（已截断）") {
		t.Fatalf("超长内容应按上限截断: %d", len(doc.Rows[0].Text))
	}
}
