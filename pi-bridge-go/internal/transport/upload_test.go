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

// 上传端点的契约。上传播送的是用户电脑上的文件，因此端点比只读端点
// 多三层守卫：目标不得已存在、路径不得落在受保护目录、隧道形态下另有
// 请求体上限。这些用例逐条钉住，避免以后「为了方便」把它们放宽。

// uploadRequest 造一个 multipart 上传请求。
func uploadRequest(t *testing.T, path string, content []byte, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("path", path); err != nil {
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

func Test上传端点写入文件(t *testing.T) {
	s, _, cwd := newTestServer(t)
	target := filepath.Join(cwd, "uploaded.txt")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, target, []byte("hello"), "uploaded.txt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("上传应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("文件未落盘：%v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("内容不符：%q", got)
	}
}

func Test上传端点拒绝覆盖(t *testing.T) {
	s, _, cwd := newTestServer(t)
	target := filepath.Join(cwd, "dup.txt")
	if err := os.WriteFile(target, []byte("原件"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, target, []byte("新"), "dup.txt"))
	if rec.Code == http.StatusOK {
		t.Fatal("覆盖已存在文件应当被拒绝")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "原件" {
		t.Fatalf("原文件被改动：%q", got)
	}
}

func Test上传端点拒绝未认证(t *testing.T) {
	s, _, cwd := newTestServer(t)
	req := uploadRequest(t, filepath.Join(cwd, "x.txt"), []byte("x"), "x.txt")
	req.Header.Del("Authorization")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应返回 401，实际 %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cwd, "x.txt")); err == nil {
		t.Fatal("未认证请求不应写入任何文件")
	}
}

func Test上传端点拒绝跨源(t *testing.T) {
	s, _, cwd := newTestServer(t)
	req := uploadRequest(t, filepath.Join(cwd, "x.txt"), []byte("x"), "x.txt")
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨源应返回 403，实际 %d", rec.Code)
	}
}

func Test上传端点拒绝根外路径(t *testing.T) {
	s, _, _ := newTestServer(t)
	outside := filepath.Join(t.TempDir(), "escape.txt")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, outside, []byte("x"), "escape.txt"))
	if rec.Code == http.StatusOK {
		t.Fatal("根外路径应当被拒绝")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("根外文件不应被创建")
	}
}

// 部署目录必须拒绝：工作区根是 /opt/helloworld，而桥自己的二进制与
// 启动脚本就在 pi-work/deploy 下。允许写入那里等于允许网页替换桥本身。
// 拒绝清单由启动方按运行时事实声明（见 upload_protect_test.go），
// 这里只验证「声明之后端点确实拦住」。
func Test上传端点拒绝部署目录(t *testing.T) {
	s, _, cwd := newTestServer(t)
	// 在测试根内模拟部署目录结构，验证判定按路径段生效。
	deploy := filepath.Join(cwd, "pi-work", "deploy")
	if err := os.MkdirAll(deploy, 0o755); err != nil {
		t.Fatal(err)
	}
	s.files.Protect(deploy)
	target := filepath.Join(deploy, "pi-bridge")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, target, []byte("#!/bin/sh\n"), "pi-bridge"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("写入部署目录应当返回 403，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("部署目录里不应出现文件")
	}
}

func Test上传端点拒绝非multipart(t *testing.T) {
	s, _, cwd := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", strings.NewReader(`{"path":"x"}`))
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("非 multipart 应被拒绝，实际 %d", rec.Code)
	}
	_ = cwd
}

func Test上传端点缺path字段时按文件名落到cwd(t *testing.T) {
	s, _, cwd := newTestServer(t)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "auto.txt")
	_, _ = part.Write([]byte("auto"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	// 没有 path 字段时用 dir 指定目录。
	q := req.URL.Query()
	q.Set("dir", cwd)
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("缺 path 时应按文件名落到 dir，实际 %d：%s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(cwd, "auto.txt"))
	if err != nil {
		t.Fatalf("文件未落盘：%v", err)
	}
	if string(got) != "auto" {
		t.Fatalf("内容不符：%q", got)
	}
}

// 文件名里带路径分隔符或 .. 时必须被清洗，不能让客户端用 filename 逃出目录。
// 清洗的语义是「取基名」：`../../evil.txt` 落到目标目录内的 evil.txt，
// 而不是报错——浏览器给出的往往是完整路径，直接拒绝会让正常的 Windows
// 上传全部失败。关键是**不能离开目标目录**。
func Test上传文件名不可越出目录(t *testing.T) {
	s, _, cwd := newTestServer(t)
	dir := filepath.Join(cwd, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "../../evil.txt")
	_, _ = part.Write([]byte("x"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	q := req.URL.Query()
	q.Set("dir", dir)
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("清洗后的文件名应能落盘，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err != nil {
		t.Fatalf("文件应落在目标目录内：%v", err)
	}
	for _, escape := range []string{filepath.Join(cwd, "evil.txt"), filepath.Join(filepath.Dir(cwd), "evil.txt")} {
		if _, err := os.Stat(escape); err == nil {
			t.Fatalf("文件逃出了目标目录：%s", escape)
		}
	}
}

// dir 必须同时接受表单字段与查询参数：前端用表单字段（FormData.set），
// curl 习惯用 -F，两者都是真实调用；只认一种会让另一种静默 400。
func Test上传dir可来自表单字段(t *testing.T) {
	s, _, cwd := newTestServer(t)
	rec := httptest.NewRecorder()
	// uploadRequest 把 path 写成表单字段；这里改用 dir + 只给文件名。
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("dir", cwd); err != nil {
		t.Fatal(err)
	}
	part, _ := w.CreateFormFile("file", "from-form.txt")
	_, _ = part.Write([]byte("content"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dir 作为表单字段时应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cwd, "from-form.txt")); err != nil {
		t.Fatalf("文件未落盘：%v", err)
	}
}

// 整个请求体必须有上限：单文件字段有 4 MiB 限制，但如果不在入口处
// 用 MaxBytesReader 卡总量，一个塞满非文件字段的请求就能绕过「4 MiB
// 上限」的直觉——文档承诺与实际行为会不一致。
func Test上传整个请求体超限时返回413(t *testing.T) {
	s, _, cwd := newTestServer(t)
	// 用**大量**非文件字段把总量撑过 MaxReadBytes + 头部余量。
	// 不能用单个超大字段：每个字段自身有 64 KiB 上限，会先被
	// 「字段超过长度上限」拦成 400，测不到总量这一层。
	per := 60 << 10
	count := int(s.files.MaxReadBytes())/per + 8
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "small.txt")
	_, _ = part.Write([]byte("x"))
	field := strings.Repeat("a", per)
	for i := 0; i < count; i++ {
		if err := w.WriteField("dir", field); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("整个请求体超限应返回 413，实际 %d：%s", rec.Code, rec.Body.String())
	}
	_ = cwd
}

// 请求体预算在**文件字段内部**耗尽时也必须是 413。
//
// 与上一条的区别：那里超限发生在非文件字段（错误由 NextPart 报出），
// 这里前面的 dir 字段吃掉一部分余量，剩下的预算在 io.Copy 读文件内容
// 时用完。两条路径必须给同一个状态码，否则同一个超限请求会因为字段
// 顺序不同得到 413 或 400（独立复审实测复现过 400）。
func Test上传预算在文件字段内耗尽时返回413(t *testing.T) {
	s, _, _ := newTestServer(t)
	// 先用多个合法长度的字段把余量吃掉：每个 60 KiB（各自都在 64 KiB
	// 上限内），共 5 个 ≈ 300 KiB，已超过总预算里的 4×64 KiB 余量。
	// 因此剩下的预算不足以读完 4 MiB 的文件内容，超限必然发生在
	// io.Copy 读文件字段期间（而不是 NextPart 处）。
	per := 60 << 10
	big := bytes.Repeat([]byte("b"), int(s.files.MaxReadBytes()))
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	field := strings.Repeat("a", per)
	for i := 0; i < 5; i++ {
		if err := w.WriteField("dir", field); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(big); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("预算在文件字段内耗尽应返回 413，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 三种目标互斥：并存时直接拒绝，不靠优先级猜。
// 尤其 base+path：base 模式根本不校验 path 是否在工作区根内，
// 若把 path 静默降级成文件名，调用方会以为它仍然生效。
func Test上传目标并存时拒绝(t *testing.T) {
	s, _, cwd := newTestServer(t)
	base := t.TempDir()
	s.files.SetUploadBases(base)
	cases := []struct {
		name   string
		fields map[string]string
	}{
		{"base+path", map[string]string{"base": base, "sessionId": "01a1ba81-1c77-72b4-bf0c-31c986eeab7b", "path": filepath.Join(cwd, "x.txt")}},
		{"base+dir", map[string]string{"base": base, "sessionId": "01a1ba81-1c77-72b4-bf0c-31c986eeab7b", "dir": cwd}},
		{"path+dir", map[string]string{"path": filepath.Join(cwd, "y.txt"), "dir": cwd}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			part, _ := w.CreateFormFile("file", "f.txt")
			_, _ = part.Write([]byte("x"))
			for k, v := range c.fields {
				_ = w.WriteField(k, v)
			}
			_ = w.Close()
			req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
			req.Host = "127.0.0.1:30142"
			req.Header.Set("Content-Type", w.FormDataContentType())
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s 应返回 400，实际 %d：%s", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// 非文件字段超长时明确报「字段超过长度上限」。
//
// 旧实现用 LimitReader 静默截断，请求继续往下走，最终报出的是
// 「目标目录不存在」这类看起来像路径写错了的错误——真实原因（字段过长）
// 被完全掩盖。截断后多读一个字节即可区分。
func Test上传非文件字段超长时明确报错(t *testing.T) {
	s, _, _ := newTestServer(t)
	huge := bytes.Repeat([]byte("a"), 100<<10)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "f.txt")
	_, _ = part.Write([]byte("x"))
	if err := w.WriteField("path", "/tmp/"+string(huge)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/ui/files/upload", &body)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("字段超长应返回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "长度上限") {
		t.Fatalf("错误信息应说明是字段过长，实际：%s", rec.Body.String())
	}
}
