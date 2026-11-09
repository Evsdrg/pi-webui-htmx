package transport

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 固定目标上传（scratch 模式）的端点契约：
// 传 base + sessionId 时，文件落到 <base>/piwebui/<会话id>/，
// 目录自动创建；基路径必须在启动方白名单里。

// scratchRequest 造一个固定目标模式的上传请求。
func scratchRequest(t *testing.T, base, sessionID, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if base != "" {
		if err := w.WriteField("base", base); err != nil {
			t.Fatal(err)
		}
	}
	if sessionID != "" {
		if err := w.WriteField("sessionId", sessionID); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}

const scratchSession = "01a1ba81-1c77-72b4-bf0c-31c986eeab7b"

func Test上传固定目标落到会话目录(t *testing.T) {
	s, _, _ := newTestServer(t)
	base := t.TempDir()
	s.files.SetUploadBases(base)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, scratchRequest(t, base, scratchSession, "note.txt", []byte("scratch")))
	if rec.Code != http.StatusOK {
		t.Fatalf("固定目标上传应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	want := filepath.Join(base, "piwebui", scratchSession, "note.txt")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("文件未落到 %s：%v", want, err)
	}
	if string(got) != "scratch" {
		t.Fatalf("内容不符：%q", got)
	}
}

// 未批准的基路径必须拒绝：否则前端传 /etc 就能写系统目录。
func Test上传固定目标拒绝未批准基路径(t *testing.T) {
	s, _, _ := newTestServer(t)
	s.files.SetUploadBases(t.TempDir())
	outside := t.TempDir()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, scratchRequest(t, outside, scratchSession, "x.txt", []byte("x")))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未批准基路径应 403，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("未批准目录里不应出现任何内容：%v", entries)
	}
}

// 会话 id 拼进路径，非法值必须挡在写之前。
func Test上传固定目标拒绝非法会话id(t *testing.T) {
	s, _, _ := newTestServer(t)
	base := t.TempDir()
	s.files.SetUploadBases(base)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, scratchRequest(t, base, "../../etc", "x.txt", []byte("x")))
	if rec.Code == http.StatusOK {
		t.Fatalf("非法会话 id 应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(base, "piwebui")); err == nil {
		t.Fatal("非法会话 id 不该建出目录")
	}
}

// 固定目标模式与当前目录模式互不干扰：只给 dir 时仍落在 dir 里。
func Test上传只给dir时不走固定目标(t *testing.T) {
	s, _, cwd := newTestServer(t)
	s.files.SetUploadBases(t.TempDir())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, filepath.Join(cwd, "plain.txt"), []byte("plain"), "plain.txt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("当前目录模式应照常工作，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cwd, "plain.txt")); err != nil {
		t.Fatalf("文件应落在 cwd：%v", err)
	}
}

// capabilities 必须上报可用的基路径，否则设置页无从得知能选什么。
func Test能力清单上报上传基路径(t *testing.T) {
	s, _, _ := newTestServer(t)
	base := t.TempDir()
	s.files.SetUploadBases(base)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "uploadBases") {
		t.Fatal("capabilities 未上报 uploadBases")
	}
	if !strings.Contains(body, filepath.Base(base)) {
		t.Fatalf("上报的基路径里没有 %s：%s", base, body[:400])
	}
}
