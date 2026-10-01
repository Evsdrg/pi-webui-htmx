package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func Test用户图片HTTP惰性读取与角色隔离(t *testing.T) {
	s, _, cwd := newTestServer(t)
	id := "user-image-test"
	image := []byte("\x89PNG\r\n\x1a\n-fixture")
	rows := []any{
		map[string]any{"type": "session", "version": 3, "id": id, "timestamp": "2026-09-28T00:00:00Z", "cwd": cwd},
		map[string]any{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "颜色？"}, map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(image)},
		}}},
		map[string]any{"type": "message", "id": "t1", "parentId": "u1", "message": map[string]any{"role": "toolResult", "content": []any{
			map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(image)},
		}}},
	}
	var data []byte
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(s.store.Dir(), id+".jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		url  string
		code int
	}{
		{"user", "/ui/sessions/" + id + "/lazy?kind=user-image&entryId=u1&blockIndex=1", 200},
		{"wrong-tool-role", "/ui/sessions/" + id + "/lazy?kind=tool-image&entryId=u1&blockIndex=1", 400},
		{"wrong-user-role", "/ui/sessions/" + id + "/lazy?kind=user-image&entryId=t1&blockIndex=0", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			s.serveLazy(rec, req, req.URL.Path)
			if rec.Code != tc.code {
				t.Fatalf("预期状态 %d，实际 %d: %s", tc.code, rec.Code, rec.Body.String())
			}
			if tc.code == 200 && (!bytes.Equal(rec.Body.Bytes(), image) || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("用户图片响应错误: type=%s cache=%s", rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
			}
		})
	}
}
