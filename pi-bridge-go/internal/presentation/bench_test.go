package presentation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/workspace"
)

// buildRenderer 造一个最小但合法的 UI 包，只用得到 sessions 与 history 两个模板。
func buildRenderer(tb testing.TB) *Renderer {
	tb.Helper()
	dir := tb.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			tb.Fatal(err)
		}
	}
	write("src/templates/sessions.html", `{{range .Items}}<a class="session-item" href="/?session={{.ID}}" data-session="{{.ID}}"><span class="session-title">{{.Title}}</span></a>{{end}}`)
	write("src/templates/history.html", `{{range .Turns}}<article class="turn" data-turn-id="{{.ID}}"><div class="turn-user"><div class="bubble">{{.UserText}}</div></div>{{range .Flow}}{{if eq .Kind "work"}}<details><summary>{{.Summary}}</summary><ol>{{range .Items}}{{if eq .Kind "tool"}}<li>{{.Step.Kind}}:{{.Step.Detail}}</li>{{end}}{{end}}</ol></details>{{else}}<div class="turn-assistant"><div class="bubble markdown">{{.Text}}</div></div>{{end}}{{end}}</article>{{end}}`)
	write("ui-manifest.json", `{"protocolVersion":1,"requiredMethods":[],"templates":{"sessions":"templates/sessions.html","history":"templates/history.html"},"build":{"entry":"src/entry/app.ts"}}`)
	write("dist/.vite/manifest.json", `{"src/entry/app.ts":{"file":"assets/app-abc123.js","isEntry":true,"css":[]}}`)
	write("dist/assets/app-abc123.js", "console.log(1)")
	r, err := LoadFromDir(dir)
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// buildPage 造一页投影好的历史记录。
func buildPage(tb testing.TB, turns int) sessions.Page {
	tb.Helper()
	cwd := tb.TempDir()
	sessionDir := filepath.Join(cwd, "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		tb.Fatal(err)
	}
	// 直接造 JSONL，字段顺序按真实 Pi：大字段在最后。
	lines := []string{`{"type":"session","version":3,"id":"p","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}`}
	parent := "null"
	for i := 0; i < turns; i++ {
		lines = append(lines,
			`{"type":"message","id":"u`+fmt.Sprint(i)+`","parentId":`+parent+`,"timestamp":"t","message":{"role":"user","content":"第 `+fmt.Sprint(i)+` 轮问题"}}`,
			`{"type":"message","id":"a`+fmt.Sprint(i)+`","parentId":"u`+fmt.Sprint(i)+`","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"第 `+fmt.Sprint(i)+` 轮回答，包含一些说明文字。"}]}}`,
			`{"type":"message","id":"r`+fmt.Sprint(i)+`","parentId":"a`+fmt.Sprint(i)+`","timestamp":"t","message":{"role":"toolResult","content":[{"type":"text","text":"`+strings.Repeat("工具输出 ", 30)+`"}]}}`,
		)
		parent = `"r` + fmt.Sprint(i) + `"`
	}
	path := filepath.Join(sessionDir, "p.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		tb.Fatal(err)
	}
	store, err := sessions.New(sessionDir, mustPolicy(tb, cwd), sessions.DefaultLimits())
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	page, err := store.History(context.Background(), "p", "", "", 50)
	if err != nil {
		tb.Fatal(err)
	}
	return page
}

// BenchmarkRenderHistory 测 HTML 片段渲染——每次翻页与首屏都要做一遍。
func BenchmarkRenderHistory(b *testing.B) {
	r := buildRenderer(b)
	page := buildPage(b, 60)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.RenderHistory("p", page); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGroupTurns 测回合聚合。
func BenchmarkGroupTurns(b *testing.B) {
	page := buildPage(b, 60)
	entries := sessions.ProjectEntries(page.Entries)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GroupTurns(entries)
	}
}

// mustPolicy 与 sessions 包的同名辅助等价，但这里是另一个包，搬一份过来。
func mustPolicy(tb testing.TB, roots ...string) *workspace.Policy {
	tb.Helper()
	p, err := workspace.New(roots)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}
