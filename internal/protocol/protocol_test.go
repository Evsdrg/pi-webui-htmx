package protocol

import (
	"encoding/json"
	"errors"
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
