package transport

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test思考正文HTML转义且保留旧JSON协议(t *testing.T) {
	s := newTestServerWithUI(t)
	rows := []any{
		map[string]any{"type": "session", "version": 3, "id": "thought", "cwd": s.files.Roots()[0]},
		map[string]any{"type": "message", "id": "a1", "parentId": nil, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "<script>unsafe</script>\nfull"}}}},
	}
	var content []byte
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		content = append(append(content, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(s.store.Dir(), "thought.jsonl"), content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"html", "json"} {
		req := httptest.NewRequest("GET", "/ui/sessions/thought/lazy?kind=thinking&entryId=a1&blockIndex=0&format="+format, nil)
		rec := httptest.NewRecorder()
		s.serveLazy(rec, req, req.URL.Path)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if format == "html" {
			if !strings.Contains(rec.Body.String(), "&lt;script&gt;") || strings.Contains(rec.Body.String(), "<script>") {
				t.Fatal(rec.Body.String())
			}
		} else {
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["thinking"] != "<script>unsafe</script>\nfull" {
				t.Fatal(err, body)
			}
		}
	}
	req := httptest.NewRequest("GET", "/ui/sessions/thought/lazy?kind=thinking&entryId=missing&blockIndex=0&format=html", nil)
	rec := httptest.NewRecorder()
	s.serveLazy(rec, req, req.URL.Path)
	if rec.Code == 200 {
		t.Fatal("失败不能伪装为成功")
	}
}
