package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 受保护目录的判定必须按**运行时事实**给出，而不是写死某个部署路径。
// 生产里桥的二进制、启动脚本与 UI 目录都在工作区根之下，允许写入那里
// 就等于允许网页替换桥自己。这些用例钉住「给什么就拒什么」，
// 以及「同一根下的普通目录不受影响」。
func Test受保护目录拒绝写入(t *testing.T) {
	s, _, cwd := newTestServer(t)
	deploy := filepath.Join(cwd, "pi-work", "deploy")
	if err := os.MkdirAll(deploy, 0o755); err != nil {
		t.Fatal(err)
	}
	s.files.Protect(deploy)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, filepath.Join(deploy, "pi-bridge"), []byte("#!/bin/sh\n"), "pi-bridge"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("写入受保护目录应返回 403，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(deploy, "pi-bridge")); err == nil {
		t.Fatal("受保护目录里不应出现文件")
	}
}

// 受保护判定是**前缀**语义：子目录也要拒绝，否则建一个 deploy/sub 就能绕开。
func Test受保护目录拒绝子目录写入(t *testing.T) {
	s, _, cwd := newTestServer(t)
	deploy := filepath.Join(cwd, "pi-work", "deploy")
	sub := filepath.Join(deploy, "ui", "assets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s.files.Protect(deploy)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, filepath.Join(sub, "x.js"), []byte("x"), "x.js"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("受保护目录的子目录也应拒绝，实际 %d", rec.Code)
	}
}

// 同名前缀但不同目录不能误伤：/a/deploy-notes 不在 /a/deploy 之下。
func Test受保护目录不误伤同名前缀(t *testing.T) {
	s, _, cwd := newTestServer(t)
	deploy := filepath.Join(cwd, "deploy")
	other := filepath.Join(cwd, "deploy-notes")
	for _, d := range []string{deploy, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.files.Protect(deploy)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, uploadRequest(t, filepath.Join(other, "note.txt"), []byte("ok"), "note.txt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy-notes 不该被 deploy 的前缀误伤，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
