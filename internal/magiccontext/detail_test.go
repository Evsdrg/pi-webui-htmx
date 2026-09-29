package magiccontext

import (
	"context"
	"strings"
	"testing"
)

func Test正文按分区映射真实表且有界(t *testing.T) {
	s, _ := withSQLite(t)
	body, err := s.Detail(context.Background(), KindDirectives, 1)
	if err != nil || body != "用中文写注释" {
		t.Fatalf("%q %v", body, err)
	}
	if _, err = s.Detail(context.Background(), KindMemories, 0); err == nil {
		t.Fatal("零ID应拒绝")
	}
	if _, err = s.Detail(context.Background(), KindMemories, 4); err == nil {
		t.Fatal("已归档记忆不可从列表全文入口读取")
	}
	if _, err = s.Detail(context.Background(), Kind("memories;DROP TABLE notes"), 1); err == nil {
		t.Fatal("非法表名应拒绝")
	}
	if _, err := runSQLite(s.dbPath, "UPDATE user_memories SET content='"+strings.Repeat("x", 65536+1)+"' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Detail(context.Background(), KindDirectives, 1); err == nil {
		t.Fatal("超长正文必须明确拒绝")
	}
}
