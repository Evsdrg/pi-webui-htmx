package events

import (
	"fmt"
	"testing"
)

func TestReplay按游标补发(t *testing.T) {
	r := NewRing(100, 1<<20)
	for i := uint64(1); i <= 5; i++ {
		r.Push(i, []byte(fmt.Sprintf(`{"seq":%d}`, i)))
	}
	items, ok := r.Replay(3)
	if !ok || len(items) != 2 || items[0].Seq != 4 || items[1].Seq != 5 {
		t.Fatalf("补发结果异常: %v %v", items, ok)
	}
	items, ok = r.Replay(0)
	if !ok || len(items) != 5 {
		t.Fatalf("从头补发异常: %v %v", items, ok)
	}
	items, ok = r.Replay(5)
	if !ok || len(items) != 0 {
		t.Fatalf("已到尾部应补空: %v %v", items, ok)
	}
}

func Test补发缺口明确失败(t *testing.T) {
	r := NewRing(3, 1<<20)
	for i := uint64(1); i <= 10; i++ {
		r.Push(i, []byte(`{"x":1}`))
	}
	// 前 7 条已被淘汰，请求 afterSeq=1 必然有缺口。
	if _, ok := r.Replay(1); ok {
		t.Fatal("序号已淘汰时必须返回 ok=false")
	}
	// 仍在窗口内的游标可以补发。
	items, ok := r.Replay(8)
	if !ok || len(items) != 2 {
		t.Fatalf("窗口内补发异常: %v %v", items, ok)
	}
}

func Test字节上限触发淘汰(t *testing.T) {
	r := NewRing(1000, 64)
	for i := uint64(1); i <= 20; i++ {
		r.Push(i, []byte("0123456789"))
	}
	stats := r.Stats()
	if stats["items"].(int) >= 20 {
		t.Fatalf("字节上限未生效: %v", stats)
	}
	if stats["bytes"].(int64) > 64+10 {
		t.Fatalf("缓冲字节超出上限: %v", stats)
	}
}

func Test空缓冲补发不报缺口(t *testing.T) {
	r := NewRing(10, 1<<20)
	if items, ok := r.Replay(0); !ok || len(items) != 0 {
		t.Fatalf("空缓冲应补空且不算缺口: %v %v", items, ok)
	}
}

func Test单条超限仍保留且记录丢弃(t *testing.T) {
	r := NewRing(10, 8)
	r.Push(1, []byte("这条事件远超字节上限"))
	stats := r.Stats()
	if stats["items"].(int) != 1 {
		t.Fatalf("单条超限不应被清空: %v", stats)
	}
	if stats["dropped"].(uint64) == 0 {
		t.Fatalf("应记录丢弃: %v", stats)
	}
}
