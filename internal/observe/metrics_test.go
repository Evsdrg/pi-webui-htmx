package observe

import (
	"sync"
	"testing"
)

func Test未知方法归入other(t *testing.T) {
	x := NewMetrics(NewMethods("session.prompt"))
	x.CommandStarted("session.prompt")
	x.CommandStarted("完全没注册过的方法")
	x.CommandStarted("另一个没注册的")
	snap := x.Snapshot()
	byMethod := snap["byMethod"].(map[string]uint64)
	if byMethod["session.prompt"] != 1 {
		t.Fatalf("已注册方法计数异常: %v", byMethod)
	}
	if byMethod["other"] != 2 {
		t.Fatalf("未注册方法应归入 other: %v", byMethod)
	}
}

func Test错误码标签受控(t *testing.T) {
	x := NewMetrics(NewMethods())
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	x.CommandFailed("m", long)
	x.CommandFailed("m", "")
	snap := x.Snapshot()
	byError := snap["byErrorCode"].(map[string]uint64)
	if len(byError) != 2 {
		t.Fatalf("错误码基数应受控: %v", byError)
	}
	if byError[long[:32]] != 1 {
		t.Fatalf("超长错误码应被截断: %v", byError)
	}
	if byError["unspecified"] != 1 {
		t.Fatalf("空错误码应归一化: %v", byError)
	}
}

func Test并发累加不丢失(t *testing.T) {
	x := NewMetrics(NewMethods("m"))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				x.CommandStarted("m")
				x.EventPublished()
			}
		}()
	}
	wg.Wait()
	snap := x.Snapshot()
	if snap["commandsTotal"].(uint64) != 3200 {
		t.Fatalf("命令计数丢失: %v", snap["commandsTotal"])
	}
	if snap["eventsTotal"].(uint64) != 3200 {
		t.Fatalf("事件计数丢失: %v", snap["eventsTotal"])
	}
}

func Test方法集合有上限(t *testing.T) {
	m := NewMethods()
	for i := 0; i < 200; i++ {
		m.Register("m" + itoa(i))
	}
	m.mu.RLock()
	n := len(m.names)
	m.mu.RUnlock()
	if n > 64 {
		t.Fatalf("方法集合应上限 64，实际 %d", n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
