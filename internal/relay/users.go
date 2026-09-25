package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Users 保存 relay 侧的用户令牌与设备预共享密钥。
// 只保留哈希，不明文落盘；内存中数量受上限约束。
type Users struct {
	secret []byte

	mu      sync.RWMutex
	byHash  map[string]string // tokenHash -> owner
	devices map[string]string // deviceID -> secretHash
	owners  map[string]struct{}
}

// NewUsers 构造用户表；secret 用于签发会话 Cookie。
func NewUsers(secret string) (*Users, error) {
	if len(secret) < 32 {
		return nil, errShortSecret
	}
	return &Users{
		secret:  []byte(secret),
		byHash:  map[string]string{},
		devices: map[string]string{},
		owners:  map[string]struct{}{},
	}, nil
}

var errShortSecret = &shortSecretError{}

type shortSecretError struct{}

func (e *shortSecretError) Error() string { return "relay 密钥至少需要 32 个字符" }

// AddUser 添加一个用户令牌，返回令牌明文（只显示一次）。
func (u *Users) AddUser(owner string) (string, error) {
	if owner == "" {
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
	return token, nil
}

// AddDeviceSecret 为设备登记预共享密钥，返回密钥明文（只显示一次）。
// 只有持有该密钥的本地桥才能向 relay 登记配对码。
func (u *Users) AddDeviceSecret(deviceID string) (string, error) {
	if deviceID == "" {
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
	if deviceID == "" || secret == "" {
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
// 属主编码进签名，服务端无需存会话即可校验归属。
func (u *Users) SignCookie(exp, owner string) string {
	m := hmac.New(sha256.New, u.secret)
	m.Write([]byte(exp + "|" + owner))
	return exp + "." + owner + "." + hex.EncodeToString(m.Sum(nil))
}

// CheckCookie 校验 Cookie 并返回属主；签名不符或属主未知时返回空。
func (u *Users) CheckCookie(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	exp, owner, sig := parts[0], parts[1], parts[2]
	if owner == "" {
		return "", false
	}
	if _, err := strconv.ParseInt(exp, 10, 64); err != nil {
		return "", false
	}
	if time.Now().Unix() >= mustInt64(exp) {
		return "", false
	}
	want := u.signature(exp, owner)
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

func (u *Users) signature(exp, owner string) string {
	m := hmac.New(sha256.New, u.secret)
	m.Write([]byte(exp + "|" + owner))
	return hex.EncodeToString(m.Sum(nil))
}

func mustInt64(v string) int64 {
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

var (
	errInvalidOwner   = &simpleError{"owner 不能为空"}
	errInvalidDevice  = &simpleError{"deviceId 不能为空"}
	errTooManyUsers   = &simpleError{"用户数量已达上限"}
	errTooManyDevices = &simpleError{"设备数量已达上限"}
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
