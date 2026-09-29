package transport

import (
	"net/http"
	"net/http/httptest"
	"pi-bridge-go/internal/protocol"
	"strings"
	"testing"
)

// 构造参数的校验必须发生在构造里，而不是散在调用方。
//
// 以前 New 收 13 个位置参数，token 与 host 相邻且同为 string：
// 对调后编译通过，运行期表现为「鉴权永远失败」。长度校验当时只在
// cmd/pi-bridge/main.go 里做，测试构造路径完全没有它。
// 现在两个字段同名不同类型也不行——它们仍是 string，但校验在构造内，
// 且 Validate 的注释写明了每个字段的约束。
func Test构造参数校验(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(o *Options)
		wantSub string
	}{
		{
			name:    "token 太短",
			mutate:  func(o *Options) { o.Token = "short" },
			wantSub: "Token",
		},
		{
			// 典型的「token 与 host 对调」：长度校验必须抓住它。
			name:    "token 位置被填成监听地址",
			mutate:  func(o *Options) { o.Token, o.Host = "127.0.0.1:30142", testToken },
			wantSub: "Token",
		},
		{
			name:    "host 为空",
			mutate:  func(o *Options) { o.Host = "" },
			wantSub: "Host",
		},
		{
			name:    "缺少 manager",
			mutate:  func(o *Options) { o.Manager = nil },
			wantSub: "Manager",
		},
		{
			name:    "缺少 store",
			mutate:  func(o *Options) { o.Store = nil },
			wantSub: "Store",
		},
		{
			name:    "缺少 files",
			mutate:  func(o *Options) { o.Files = nil },
			wantSub: "Files",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := testOptions(t)
			c.mutate(&opts)
			if err := opts.Validate(); err == nil {
				t.Fatalf("应报错（%s），但通过了校验", c.name)
			} else if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("错误信息应点名 %s，得到 %q", c.wantSub, err.Error())
			}
		})
	}
	if err := testOptions(t).Validate(); err != nil {
		t.Fatalf("合法参数不应报错：%v", err)
	}
}

// UI 层是可选的（--ui-dir 未给时禁用），因此它不在必填项里；
// 但一旦提供，就必须是可用的渲染器。
func TestUI层可选但缺了要有明确表现(t *testing.T) {
	opts := testOptions(t)
	opts.UI = nil
	if err := opts.Validate(); err != nil {
		t.Fatalf("UI 缺省不应算参数错误：%v", err)
	}
}

// UI 层缺席时，片段路径必须给出可读文本而不是 panic。
// 渲染器本身不再容忍 nil 接收者，所以这条守卫是必要的。
func TestUI缺席时片段路径不panic(t *testing.T) {
	opts := testOptions(t)
	opts.UI = nil
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.fragmentIssue(rec, "", protocol.E("not_found", "会话不存在"))
	if rec.Code != 200 {
		t.Fatalf("片段端点应回 200，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "会话不存在") {
		t.Fatalf("应给出可读文本：%s", rec.Body.String())
	}
	// /ui/* 在 UI 缺席时整体 404，调用方据此继续。
	req := httptest.NewRequest(http.MethodGet, "/ui/sessions/x/history", nil)
	req.Host = "127.0.0.1:30142"
	req.Header.Set("Authorization", "Bearer "+testToken)
	uiRec := httptest.NewRecorder()
	srv.ServeHTTP(uiRec, req)
	if uiRec.Code != http.StatusNotFound {
		t.Fatalf("未配置 UI 包时 /ui/* 应回 404，得到 %d", uiRec.Code)
	}
}
