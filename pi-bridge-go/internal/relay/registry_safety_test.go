package relay

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// Test过期配对码不能领取 覆盖 B20：
// ClaimTTL 以前只作为 expiresInSeconds 告知客户端，服务端不校验年龄，
// 泄露的配对码可被无限期领取。
func Test过期配对码不能领取(t *testing.T) {
	limits := DefaultLimits()
	limits.ClaimTTL = 50 * time.Millisecond
	r, err := NewRegistry(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	code, err := r.Register("dev-1", "机器")
	if err != nil {
		t.Fatal(err)
	}
	// 等到配对码过期。
	time.Sleep(80 * time.Millisecond)
	if _, _, err := r.Claim("alice", code); err == nil {
		t.Fatal("过期的配对码仍被领取")
	}
	// 过期后必须作废，不能再被第二次使用。
	r.mu.Lock()
	left := r.devices["dev-1"].PairingCode
	r.mu.Unlock()
	if left != "" {
		t.Fatal("过期的配对码没有被作废")
	}
}

// Test未过期配对码可正常领取 覆盖反向边界。
func Test未过期配对码可正常领取(t *testing.T) {
	limits := DefaultLimits()
	limits.ClaimTTL = 10 * time.Minute
	r, err := NewRegistry(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	code, err := r.Register("dev-2", "机器")
	if err != nil {
		t.Fatal(err)
	}
	dev, token, err := r.Claim("alice", code)
	if err != nil {
		t.Fatalf("未过期的配对码应可领取: %v", err)
	}
	if dev.Owner != "alice" || token == "" {
		t.Fatalf("领取结果异常: %+v", dev)
	}
}

// Test配对尝试表有活跃上限 覆盖 B21：
// 尝试表按客户端提交的 code 分桶，没有硬上限时可被撑到任意大小。
func Test配对尝试表有活跃上限(t *testing.T) {
	r := newRegistry(t)
	// 用互不相同但都非法的 code 灌表。
	for i := 0; i < maxPairAttempts+200; i++ {
		code := string(rune('A'+i%26)) + string(rune('A'+(i/26)%26)) + "123456"
		_, _, _ = r.Claim("alice", code)
	}
	r.mu.Lock()
	n := len(r.attempts)
	r.mu.Unlock()
	if n > maxPairAttempts {
		t.Fatalf("配对尝试表超过活跃上限: %d > %d", n, maxPairAttempts)
	}
}

// Test持久化与在线更新无竞争 覆盖 B22：
// persist 解锁后仍序列化 *Device，SetOnline 会并发写同一字段。
// 这条在 -race 下才能暴露。
func Test持久化与在线更新无竞争(t *testing.T) {
	limits := DefaultLimits()
	limits.PersistInterval = time.Millisecond
	r, err := NewRegistry(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	for i := 0; i < 8; i++ {
		if _, err := r.Register("dev-"+strings.Repeat("x", i+1), "机器"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// 持续触发持久化。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.persist()
			}
		}
	}()
	// 并发更新在线状态与心跳。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.SetOnline("dev-"+strings.Repeat("x", id+1), true)
				r.mu.Lock()
				for _, d := range r.devices {
					d.LastSeen = time.Now().UTC()
					d.Online = !d.Online
				}
				r.mu.Unlock()
			}
		}(i)
	}
	// 等在线更新跑完再停持久化，保证两者有充分重叠。
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
