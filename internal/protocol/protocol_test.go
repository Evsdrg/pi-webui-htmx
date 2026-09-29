package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecode拒绝未知字段与多值(t *testing.T) {
	var dst struct {
		Cwd string `json:"cwd"`
	}
	if err := Decode([]byte(`{"cwd":"/tmp","extra":1}`), &dst); err == nil {
		t.Fatal("未知字段应被拒绝")
	}
	if err := Decode([]byte(`{"cwd":"/tmp"}{"cwd":"/x"}`), &dst); err == nil {
		t.Fatal("多个 JSON 值应被拒绝")
	}
	if err := Decode([]byte(`null`), &dst); err == nil {
		t.Fatal("null 应被拒绝")
	}
	if err := Decode(nil, &dst); err != nil {
		t.Fatalf("空参数应视为空对象: %v", err)
	}
	if err := Decode([]byte(`{"cwd":"/tmp"}`), &dst); err != nil || dst.Cwd != "/tmp" {
		t.Fatalf("正常参数解析失败: %v %+v", err, dst)
	}
}

func TestReply失败时不带数据(t *testing.T) {
	m := Reply("r1", map[string]any{"partial": true}, E("busy", "忙"))
	if m.OK == nil || *m.OK {
		t.Fatal("失败响应 ok 必须为 false")
	}
	if m.Data != nil {
		t.Fatalf("失败响应不应携带数据: %v", m.Data)
	}
	if m.Error == nil || m.Error.Code != "busy" || m.Error.Message != "忙" {
		t.Fatalf("错误信息不符合要求: %v", m.Error)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" {
		t.Fatal("响应序列化为空")
	}
}

func TestReply未知错误不泄露细节(t *testing.T) {
	m := Reply("r1", nil, errors.New("内部路径 /home/user/secret 泄漏"))
	if m.Error == nil || m.Error.Message != "操作失败" {
		t.Fatalf("未知错误应泛化: %v", m.Error)
	}
}

// Wrap 是「对外仍是稳定错误码，对内保留原因」的入口。
// 它必须满足三件事：能回溯原因、外层码优先、原因不进 JSON。
func TestWrap保留原因且外层码优先(t *testing.T) {
	cause := errors.New("dial tcp 10.0.0.1:443: connection refused")
	err := Wrap("pi_error", "请求供应商失败", cause)
	if !errors.Is(err, cause) {
		t.Fatal("必须能用 errors.Is 回溯到原因")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "pi_error" {
		t.Fatalf("errors.As 应拿到外层协议错误: %+v", pe)
	}
	if !strings.Contains(err.Error(), "请求供应商失败") {
		t.Fatalf("错误文本应包含对外提示: %q", err.Error())
	}
}

// 判定只看最外层：否则一次包装就能把「not_found」塞给调用方，
// 而 server.go 的 204 分支、claims 的重试判定都是按码分支的。
func Test错误码判定只看最外层(t *testing.T) {
	inner := E("not_found", "会话文件不存在")
	outer := Wrap("busy", "工作进程仍在忙", inner)
	var pe *Error
	if !errors.As(outer, &pe) {
		t.Fatal("应能取到协议错误")
	}
	if pe.Code != "busy" {
		t.Fatalf("最外层码必须是 busy，得到 %q——内层码不得劫持判定", pe.Code)
	}
	if !errors.Is(outer, inner) {
		t.Fatal("原因链仍需可回溯（排查用）")
	}
}

// 原因可能含本机路径、上游原文或凭据；它们既不能进 WS 响应，也不能进片段提示。
func Test错误原因不进入JSON(t *testing.T) {
	err := Wrap("pi_error", "请求供应商失败", errors.New("Authorization: Bearer sk-secret-value"))
	b, mErr := json.Marshal(err)
	if mErr != nil {
		t.Fatal(mErr)
	}
	if strings.Contains(string(b), "sk-secret-value") || strings.Contains(string(b), "Bearer") {
		t.Fatalf("序列化不得带出原因: %s", b)
	}
	var fields map[string]any
	if uErr := json.Unmarshal(b, &fields); uErr != nil {
		t.Fatal(uErr)
	}
	if len(fields) != 2 || fields["code"] != "pi_error" || fields["message"] != "请求供应商失败" {
		t.Fatalf("错误对象字段应只有 code/message: %v", fields)
	}
}
