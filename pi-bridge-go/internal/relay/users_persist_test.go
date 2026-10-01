package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test用户凭据跨进程存活 覆盖 B19：
// --add-user 是一次性 CLI 进程，加完即退。若用户表只活在内存，
// 服务进程重建后刚签发的令牌与预共享密钥全部失效。
func Test用户凭据跨进程存活(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	const owner = "alice"

	first, err := NewUsers(testRelaySecret, path)
	if err != nil {
		t.Fatal(err)
	}
	token, err := first.AddUser(owner)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := first.AddDeviceSecret("dev-1")
	if err != nil {
		t.Fatal(err)
	}

	// 模拟「CLI 进程退出、服务进程重启」：全新实例从同一路径加载。
	second, err := NewUsers(testRelaySecret, path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := second.Check(token)
	if !ok || got != owner {
		t.Fatalf("重启后用户令牌失效: %q %v", got, ok)
	}
	if !second.CheckPairSecret("dev-1", secret) {
		t.Fatal("重启后设备预共享密钥失效")
	}
	// 密钥不应明文落盘。
	raw := readFileForTest(t, path)
	if containsForTest(raw, token) || containsForTest(raw, secret) {
		t.Fatal("用户表泄露了明文凭据")
	}
}

// Test含点属主可签发与校验Cookie 覆盖 B46：
// 旧实现用 '.' 分隔，含 '.' 的属主让 CheckCookie 得到 4 段，认证永远失败。
func Test含点属主可签发与校验Cookie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	u, err := NewUsers(testRelaySecret, path)
	if err != nil {
		t.Fatal(err)
	}
	const owner = "user.name@example.com"
	if _, err := u.AddUser(owner); err != nil {
		t.Fatal(err)
	}
	cookie := u.SignCookie("9999999999", owner)
	got, ok := u.CheckCookie(cookie)
	if !ok || got != owner {
		t.Fatalf("含点属主无法通过 Cookie 校验: %q %v", got, ok)
	}
}

// Test非法属主被拒绝 覆盖 B46 的另一半：
// 控制字符与空白会破坏日志与诊断输出，必须在入口拒绝。
func Test非法属主被拒绝(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	u, err := NewUsers(testRelaySecret, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "a b", "a\tb", "a/b", "a:b", "a;b", "中文", string(make([]byte, 200))} {
		if _, err := u.AddUser(bad); err == nil {
			t.Fatalf("含非法字符的属主应被拒绝: %q", bad)
		}
		if _, err := u.AddDeviceSecret(bad); err == nil {
			t.Fatalf("含非法字符的设备 ID 应被拒绝: %q", bad)
		}
	}
	// 合法字符集合必须可用。
	for _, ok := range []string{"a-b_c@d", "Alice42", "x"} {
		if _, err := u.AddUser(ok); err != nil {
			t.Fatalf("合法属主被拒绝: %q: %v", ok, err)
		}
	}
}

// Test落盘失败不回滚内存外的状态 覆盖持久化失败路径：
// AddUser 落盘失败时必须回滚，不能让 CLI 以为添加成功。
func Test落盘失败不回滚内存外的状态(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	// 先成功写入一次，让 load 能通过。
	if _, err := NewUsers(testRelaySecret, path); err != nil {
		t.Fatal(err)
	}
	// 再把目录设为只读：CreateTemp 与 rename 都会失败，而 load 仍然可读。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	u, err := NewUsers(testRelaySecret, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.AddUser("bob"); err == nil {
		t.Fatal("目录只读时写入应报错")
	}
	// 落盘失败必须回滚：用户表保持为空，CLI 也不会误以为添加成功。
	if n := u.Stats()["users"].(int); n != 0 {
		t.Fatalf("失败后用户表应保持为空，实际 %d", n)
	}
	if _, ok := u.Check("usr_bob"); ok {
		t.Fatal("失败后仍能校验到该用户，说明没有回滚")
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsForTest(haystack, needle string) bool {
	return len(needle) > 0 && strings.Contains(haystack, needle)
}
