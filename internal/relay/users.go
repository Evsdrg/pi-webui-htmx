package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Users 保存 relay 侧的用户令牌与设备预共享密钥。
//
// 持久化是必须的：--add-user / --add-device 是一次性 CLI 进程，
// 加完即退。若用户表只活在内存，服务进程重建时表为空，
// 刚签发的令牌与预共享密钥全部失效（B19）。
// 只落哈希，不明文；写入是「临时文件 + rename」的原子替换。
type Users struct {
	secret []byte
	path   string

	mu      sync.RWMutex
	byHash  map[string]string // tokenHash -> owner
	devices map[string]string // deviceID -> secretHash
	owners  map[string]struct{}
}

// usersFile 是落盘格式。版本号用于将来扩展时能识别旧文件。
type usersFile struct {
	Version int               `json:"version"`
	Owners  []string          `json:"owners"`
	Tokens  map[string]string `json:"tokens"`  // tokenHash -> owner
	Devices map[string]string `json:"devices"` // deviceID -> secretHash
}

const usersFileVersion = 1

// validOwnerChars 限定属主可用字符。
//
// 为什么仍要限定：属主会出现在日志与诊断输出里，控制字符与空白
// 会破坏可读性与可解析性。'.' 是允许的——旧实现用 '.' 做 Cookie 分隔符，
// 含 '.' 的属主会让 CheckCookie 得到 4 段、认证永远失败（B46）；
// 现在属主改为 base64url 编码，分隔符冲突已消除，不必再牺牲合法字符。
func validOwner(owner string) bool {
	if owner == "" || len(owner) > 64 {
		return false
	}
	for _, ch := range owner {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '@', ch == '.':
		default:
			return false
		}
	}
	return true
}

// validDeviceID 限定设备 ID 可用字符。设备 ID 会出现在 URL 查询串里，
// 必须排除分隔符与控制字符，避免注入或日志歧义。
func validDeviceID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '-', ch == '_':
		default:
			return false
		}
	}
	return true
}

// NewUsers 构造用户表；secret 用于签发会话 Cookie。
// path 为空表示不持久化（仅用于测试）。
func NewUsers(secret, path string) (*Users, error) {
	if len(secret) < 32 {
		return nil, errShortSecret
	}
	u := &Users{
		secret:  []byte(secret),
		path:    path,
		byHash:  map[string]string{},
		devices: map[string]string{},
		owners:  map[string]struct{}{},
	}
	if path != "" {
		if err := u.load(); err != nil {
			return nil, err
		}
	}
	return u, nil
}

var errShortSecret = &shortSecretError{}

type shortSecretError struct{}

func (e *shortSecretError) Error() string { return "relay 密钥至少需要 32 个字符" }

// load 从磁盘恢复用户表。文件不存在是正常的首次启动。
// 文件损坏时明确报错，不静默清空——那会让已签发的令牌看似有效实则失效。
func (u *Users) load() error {
	b, err := os.ReadFile(u.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f usersFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if f.Version != usersFileVersion {
		return errUsersVersion
	}
	for _, owner := range f.Owners {
		if !validOwner(owner) {
			return errUsersCorrupt
		}
		u.owners[owner] = struct{}{}
	}
	for hash, owner := range f.Tokens {
		if _, ok := u.owners[owner]; !ok {
			return errUsersCorrupt
		}
		u.byHash[hash] = owner
	}
	for id := range f.Devices {
		if !validDeviceID(id) {
			return errUsersCorrupt
		}
	}
	for id, h := range f.Devices {
		u.devices[id] = h
	}
	return nil
}

// persistLocked 原子写盘。调用方必须已持有写锁。
func (u *Users) persistLocked() error {
	if u.path == "" {
		return nil
	}
	f := usersFile{
		Version: usersFileVersion,
		Owners:  make([]string, 0, len(u.owners)),
		Tokens:  u.byHash,
		Devices: u.devices,
	}
	for owner := range u.owners {
		f.Owners = append(f.Owners, owner)
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0o700); err != nil {
		return err
	}
	// 私有临时文件 + rename：既避免半写文件被当成有效状态，
	// 也不在共享临时目录留下可被他人读取的令牌哈希。
	tmp, err := os.CreateTemp(filepath.Dir(u.path), ".users-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), u.path)
}

// AddUser 添加一个用户令牌，返回令牌明文（只显示一次）。
func (u *Users) AddUser(owner string) (string, error) {
	if !validOwner(owner) {
		return "", errInvalidOwner
	}
	token, err := randomToken("usr_", 24)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.byHash) >= 64 {
		return "", errTooManyUsers
	}
	u.byHash[hashToken(token)] = owner
	u.owners[owner] = struct{}{}
	if err := u.persistLocked(); err != nil {
		// 回滚内存改动：落盘失败就不能让 CLI 以为添加成功。
		delete(u.byHash, hashToken(token))
		if u.countOwnersLocked(owner) == 0 {
			delete(u.owners, owner)
		}
		return "", err
	}
	return token, nil
}

// countOwnersLocked 统计某属主名下还有多少令牌。调用方需持锁。
func (u *Users) countOwnersLocked(owner string) int {
	n := 0
	for _, o := range u.byHash {
		if o == owner {
			n++
		}
	}
	return n
}

// AddDeviceSecret 为设备登记预共享密钥，返回密钥明文（只显示一次）。
// 只有持有该密钥的本地桥才能向 relay 登记配对码。
func (u *Users) AddDeviceSecret(deviceID string) (string, error) {
	if !validDeviceID(deviceID) {
		return "", errInvalidDevice
	}
	secret, err := randomToken("sec_", 24)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.devices) >= 256 {
		return "", errTooManyDevices
	}
	u.devices[deviceID] = hashToken(secret)
	if err := u.persistLocked(); err != nil {
		delete(u.devices, deviceID)
		return "", err
	}
	return secret, nil
}

// Check 校验用户令牌并返回属主。
func (u *Users) Check(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	owner, ok := u.byHash[hashToken(token)]
	return owner, ok
}

// CheckPairSecret 校验设备预共享密钥。
func (u *Users) CheckPairSecret(deviceID, secret string) bool {
	if !validDeviceID(deviceID) || secret == "" {
		return false
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	want, ok := u.devices[deviceID]
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(hashToken(secret))) == 1
}

// SignCookie 签发 Cookie 值：过期时间.属主.签名。
// 属主用 base64url 编码，彻底消除 '.' 之类的分隔符冲突（B46）。
func (u *Users) SignCookie(exp, owner string) string {
	enc := base64.RawURLEncoding.EncodeToString([]byte(owner))
	m := hmac.New(sha256.New, u.secret)
	m.Write([]byte(exp + "|" + enc))
	return exp + "." + enc + "." + hex.EncodeToString(m.Sum(nil))
}

// CheckCookie 校验 Cookie 并返回属主；签名不符或属主未知时返回空。
func (u *Users) CheckCookie(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	exp, enc, sig := parts[0], parts[1], parts[2]
	if enc == "" {
		return "", false
	}
	ownerBytes, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	owner := string(ownerBytes)
	if !validOwner(owner) {
		return "", false
	}
	if _, err := strconv.ParseInt(exp, 10, 64); err != nil {
		return "", false
	}
	if time.Now().Unix() >= mustInt64(exp) {
		return "", false
	}
	want := u.signature(exp, enc)
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return "", false
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	if _, known := u.owners[owner]; !known {
		return "", false
	}
	return owner, true
}

func (u *Users) signature(exp, enc string) string {
	m := hmac.New(sha256.New, u.secret)
	m.Write([]byte(exp + "|" + enc))
	return hex.EncodeToString(m.Sum(nil))
}

func mustInt64(v string) int64 {
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

var (
	errInvalidOwner   = &simpleError{"owner 含有不允许的字符"}
	errInvalidDevice  = &simpleError{"deviceId 含有不允许的字符"}
	errTooManyUsers   = &simpleError{"用户数量已达上限"}
	errTooManyDevices = &simpleError{"设备数量已达上限"}
	errUsersVersion   = &simpleError{"用户表版本不受支持"}
	errUsersCorrupt   = &simpleError{"用户表内容损坏"}
)

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

func randomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// Stats 返回用户表规模。
func (u *Users) Stats() map[string]any {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return map[string]any{"users": len(u.byHash), "devices": len(u.devices)}
}
