package relay

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(t.TempDir(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func Test配对码仅能使用一次(t *testing.T) {
	r := newRegistry(t)
	code, err := r.Register("dev-1", "我的机器")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 8 {
		t.Fatalf("配对码长度异常: %q", code)
	}
	// 字母表已排除 0/O/1/I，避免手抄时混淆。
	for _, forbidden := range "01OI" {
		if strings.ContainsRune(code, forbidden) {
			t.Fatalf("配对码包含易混淆字符 %q: %q", forbidden, code)
		}
	}
	device, token, err := r.Claim("alice", code)
	if err != nil {
		t.Fatal(err)
	}
	if device.Owner != "alice" || token == "" {
		t.Fatalf("配对结果异常: %+v", device)
	}
	// 同一配对码不能二次使用。
	if _, _, err := r.Claim("bob", code); err == nil {
		t.Fatal("配对码应一次性失效")
	}
}

func Test配对尝试限流(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	blocked := false
	for i := 0; i < 20; i++ {
		if _, _, err := r.Claim("alice", "WRONGCOD"); err != nil {
			if strings.Contains(err.Error(), "配对尝试过于频繁") {
				blocked = true
				break
			}
		}
	}
	if !blocked {
		t.Fatal("暴力尝试应被限流")
	}
}

func Test设备令牌校验(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	code, err := r.Register("dev-2", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = code
	// 直接构造已归属设备。
	r.mu.Lock()
	r.devices["dev-1"].Owner = "alice"
	r.devices["dev-1"].TokenHash = hashToken("token-abc")
	r.mu.Unlock()
	d, err := r.AuthenticateDevice("dev-1", "token-abc")
	if err != nil || d.DeviceID != "dev-1" {
		t.Fatalf("正确令牌应通过: %v %+v", err, d)
	}
	if _, err := r.AuthenticateDevice("dev-1", "wrong"); err == nil {
		t.Fatal("错误令牌必须被拒绝")
	}
	if _, err := r.AuthenticateDevice("dev-9", "token-abc"); err == nil {
		t.Fatal("不存在的设备必须被拒绝")
	}
}

func Test未归属设备不能建隧道(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-new", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AuthenticateDevice("dev-new", "anything"); err == nil {
		t.Fatal("未配对设备不得通过设备令牌鉴权")
	}
}

func Test设备数量上限(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxDevices = 3
	r, err := NewRegistry(t.TempDir(), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 3; i++ {
		if _, err := r.Register("dev-"+string(rune('a'+i)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Register("dev-over", ""); err == nil {
		t.Fatal("超过设备上限必须被拒绝")
	}
}

func Test重新登记换码且旧码失效(t *testing.T) {
	r := newRegistry(t)
	first, err := r.Register("dev-1", "A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Register("dev-1", "B")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("重新登记应生成新配对码")
	}
	if _, _, err := r.Claim("alice", first); err == nil {
		t.Fatal("旧配对码应立即失效")
	}
	if _, _, err := r.Claim("alice", second); err != nil {
		t.Fatalf("新配对码应可用: %v", err)
	}
}

func Test重启后归属保留(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRegistry(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	code, err := r.Register("dev-1", "机器")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Claim("alice", code); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDeviceToken("dev-1", "tok-1"); err != nil {
		t.Fatal(err)
	}
	r.dirty = true
	if err := r.persist(); err != nil {
		t.Fatal(err)
	}
	r.Close()

	r2, err := NewRegistry(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	owner, err := r2.Owner("dev-1")
	if err != nil || owner != "alice" {
		t.Fatalf("重启后归属应保留: %q %v", owner, err)
	}
	if _, err := r2.AuthenticateDevice("dev-1", "tok-1"); err != nil {
		t.Fatalf("重启后设备令牌应仍有效: %v", err)
	}
	// 重启后不应仍处于在线状态。
	r2.mu.Lock()
	online := r2.devices["dev-1"].Online
	r2.mu.Unlock()
	if online {
		t.Fatal("重启后不得保留在线状态")
	}
}

func Test撤销后令牌失效(t *testing.T) {
	r := newRegistry(t)
	code, err := r.Register("dev-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Claim("alice", code); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDeviceToken("dev-1", "tok"); err != nil {
		t.Fatal(err)
	}
	if err := r.Revoke("alice", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AuthenticateDevice("dev-1", "tok"); err == nil {
		t.Fatal("撤销后设备令牌必须失效")
	}
	if err := r.Revoke("bob", "dev-1"); err == nil {
		t.Fatal("非属主撤销必须被拒绝")
	}
}

func Test并发配对不产生双归属(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	code, err := r.Register("dev-1", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	owners := make([]string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := r.Claim("user"+string(rune('a'+i)), code); err == nil {
				owners[i] = "claimed"
			}
		}(i)
	}
	wg.Wait()
	claimed := 0
	for _, v := range owners {
		if v == "claimed" {
			claimed++
		}
	}
	if claimed > 1 {
		t.Fatalf("同一配对码只能成功一次，实际 %d 次", claimed)
	}
	owner, err := r.Owner("dev-1")
	if err != nil || owner == "" {
		t.Fatalf("应存在唯一属主: %q %v", owner, err)
	}
}

func Test注册表损坏时显式报错(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/devices.json", "不是 JSON"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(dir, DefaultLimits()); err == nil {
		t.Fatal("损坏的注册表必须显式报错，不能静默重置")
	}
}

func TestPersist原子写(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRegistry(dir, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.persist(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(dir + "/devices.json") {
		t.Fatal("注册表未落盘")
	}
	if fileExists(dir + "/devices.json.tmp") {
		t.Fatal("临时文件应已被替换")
	}
}

func TestStats规模(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	code, err := r.Register("dev-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Claim("alice", code); err != nil {
		t.Fatal(err)
	}
	r.SetOnline("dev-2", true)
	stats := r.Stats()
	if stats["devices"].(int) != 2 || stats["claimed"].(int) != 1 || stats["online"].(int) != 1 {
		t.Fatalf("统计异常: %v", stats)
	}
	if got := r.List("alice"); len(got) != 1 || got[0].DeviceID != "dev-2" {
		t.Fatalf("按属主列表异常: %+v", got)
	}
	if got := r.List("bob"); len(got) != 0 {
		t.Fatalf("其他用户不应看到设备: %+v", got)
	}
}

func TestLastSeen更新(t *testing.T) {
	r := newRegistry(t)
	if _, err := r.Register("dev-1", ""); err != nil {
		t.Fatal(err)
	}
	before := r.devices["dev-1"].LastSeen
	time.Sleep(5 * time.Millisecond)
	r.SetOnline("dev-1", true)
	after := r.devices["dev-1"].LastSeen
	if !after.After(before) {
		t.Fatal("LastSeen 未更新")
	}
}
