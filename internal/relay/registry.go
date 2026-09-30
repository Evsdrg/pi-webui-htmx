// Package relay 实现云端转发器：把浏览器连接路由到本地桥的主动隧道。
// 硬约束：relay 只搬运字节，不解析协议、不落盘会话正文、不接触模型密钥。
package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pi-bridge-go/internal/protocol"
)

// Device 是一台已配对或待配对的本地桥。
type Device struct {
	DeviceID    string `json:"deviceId"`
	Name        string `json:"name,omitempty"`
	Owner       string `json:"owner,omitempty"`
	PairingCode string `json:"-"`
	// PairingAt 是配对码的签发时间，仅存内存、不落盘：
	// 进程重启后所有未使用的配对码一律失效，比持久化时间更安全。
	PairingAt time.Time `json:"-"`
	ClaimedAt time.Time `json:"claimedAt,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	Online    bool      `json:"online"`
	TokenHash string    `json:"tokenHash,omitempty"`
}

// tokenMatches 常量时间比对令牌哈希。
func (d *Device) tokenMatches(token string) bool {
	if d.TokenHash == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(hashToken(token))) == 1
}

// hashToken 用 SHA-256 保存令牌，不明文落盘。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Limits 约束注册表规模与配对尝试次数，避免被刷爆。
type Limits struct {
	MaxDevices      int
	ClaimTTL        time.Duration
	PairAttempts    int
	PairWindow      time.Duration
	PersistInterval time.Duration
}

// maxPairAttempts 是配对尝试表的活跃项硬上限。
// 按 code 分桶，且 code 由客户端提交：没有硬上限时，
// 一批各不相同的 code 可以把表撑到任意大小（B21）。
const maxPairAttempts = 1024

// DefaultLimits 给出默认限额。
func DefaultLimits() Limits {
	return Limits{
		MaxDevices:      256,
		ClaimTTL:        15 * time.Minute,
		PairAttempts:    5,
		PairWindow:      time.Minute,
		PersistInterval: 30 * time.Second,
	}
}

// Registry 保存设备、配对码与归属关系。
// 只存必要的少量元数据；每条记录都是有界的，总数受 MaxDevices 限制。
type Registry struct {
	dir    string
	limits Limits

	mu       sync.Mutex
	devices  map[string]*Device
	attempts map[string]*attemptWindow
	dirty    bool
	// version 每次置脏自增，persist 靠它判断「写盘期间是否有新改动」，
	// 决定能不能清脏（B78）。
	version uint64
	closed  bool
}

type attemptWindow struct {
	count       int
	windowStart time.Time
}

// NewRegistry 打开或创建注册表。
func NewRegistry(dir string, limits Limits) (*Registry, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r := &Registry{dir: dir, limits: limits, devices: map[string]*Device{}, attempts: map[string]*attemptWindow{}}
	if err := r.load(); err != nil {
		return nil, err
	}
	go r.persistLoop()
	return r, nil
}

func (r *Registry) path() string { return filepath.Join(r.dir, "devices.json") }

// markDirty 记录有待写盘的改动；调用方必须已持有 r.mu。
func (r *Registry) markDirty() {
	r.dirty = true
	r.version++
}

// load 只在启动时读一次小文件，不做全量扫描。
func (r *Registry) load() error {
	b, err := os.ReadFile(r.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) > 1<<20 {
		return protocol.E("limit_exceeded", "注册表文件过大")
	}
	var list []*Device
	if json.Unmarshal(b, &list) != nil {
		// 损坏的注册表不应阻止启动，但必须显式告知。
		return protocol.E("invalid_history", "注册表文件损坏")
	}
	for _, d := range list {
		if d.DeviceID == "" || len(r.devices) >= r.limits.MaxDevices {
			continue
		}
		d.Online = false
		r.devices[d.DeviceID] = d
	}
	return nil
}

// Close 停止持久化协程并做最后一次落盘。
func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	_ = r.persist()
}

func (r *Registry) persistLoop() {
	interval := r.limits.PersistInterval
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range tick.C {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return
		}
		_ = r.persist()
	}
}

// persist 原子写盘，避免半截文件被下次启动读入。
func (r *Registry) persist() error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	list := make([]*Device, 0, len(r.devices))
	for _, d := range r.devices {
		list = append(list, d)
	}
	// 必须在锁内序列化：list 里是 *Device 指针，解锁后 SetOnline
	// 会与 marshal 并发读写同一字段（B22，-race 已复现）。
	b, err := json.MarshalIndent(list, "", "  ")
	version := r.version
	r.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := r.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path()); err != nil {
		return err
	}
	// 落盘成功之后才清脏，而且只清「写下的就是当前状态」那一份：
	// 写盘期间又发生的改动要留给下一次（B78）。
	r.mu.Lock()
	if r.version == version {
		r.dirty = false
	}
	r.mu.Unlock()
	return nil
}

func randomID(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// PairingCode 生成人类可输入的一次性配对码。
// 只用无歧义字符，避免 0/O、1/l 抄错。
const pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func pairingCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, v := range b {
		out[i] = pairingAlphabet[int(v)%len(pairingAlphabet)]
	}
	return string(out), nil
}

// Register 登记一台等待配对的设备，返回配对码。
// 重复登记同一 deviceId 时会换一个新的配对码，旧码立即失效。
func (r *Registry) Register(deviceID, name string) (string, error) {
	if deviceID == "" || len(deviceID) > 128 {
		return "", protocol.E("invalid_params", "deviceId 无效")
	}
	if len(name) > 64 {
		name = name[:64]
	}
	code, err := pairingCode()
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", protocol.E("worker_exited", "转发器正在关闭")
	}
	existing := r.devices[deviceID]
	if existing == nil {
		if len(r.devices) >= r.limits.MaxDevices {
			return "", protocol.E("limit_exceeded", "设备数量已达上限")
		}
		now := time.Now().UTC()
		r.devices[deviceID] = &Device{
			DeviceID: deviceID, Name: name, PairingCode: code, PairingAt: now, CreatedAt: now, LastSeen: now,
		}
	} else {
		// 已归属的设备重新登记时保留归属，只重置配对码。
		existing.PairingCode = code
		existing.PairingAt = time.Now().UTC()
		existing.Name = name
		existing.LastSeen = time.Now().UTC()
	}
	r.markDirty()
	return code, nil
}

// allowAttempt 记录一次配对尝试，超限返回 false。
// 按配对码维度限流，防止暴力枚举。
func (r *Registry) allowAttempt(code string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	a, ok := r.attempts[code]
	if !ok || now.Sub(a.windowStart) > r.limits.PairWindow {
		// 先清理过期项，再看活跃上限。旧实现只在超限后清理，
		// 于是一批各不相同的 code 可以把表撑到 4096 才滚动驱逐（B21）。
		for k, v := range r.attempts {
			if now.Sub(v.windowStart) > r.limits.PairWindow {
				delete(r.attempts, k)
			}
		}
		if len(r.attempts) >= maxPairAttempts {
			return false
		}
		r.attempts[code] = &attemptWindow{count: 1, windowStart: now}
		return true
	}
	a.count++
	return a.count <= r.limits.PairAttempts
}

// Claim 用配对码把设备绑定到某个用户，返回设备令牌。
func (r *Registry) Claim(owner, code string) (*Device, string, error) {
	if owner == "" || code == "" {
		return nil, "", protocol.E("invalid_params", "owner 与配对码均不能为空")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 8 {
		return nil, "", protocol.E("invalid_params", "配对码格式不正确")
	}
	if !r.allowAttempt(code) {
		return nil, "", protocol.E("limit_exceeded", "配对尝试过于频繁，请稍后再试")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, "", protocol.E("worker_exited", "转发器正在关闭")
	}
	var target *Device
	for _, d := range r.devices {
		if d.PairingCode != "" && subtle.ConstantTimeCompare([]byte(d.PairingCode), []byte(code)) == 1 {
			target = d
			break
		}
	}
	if target == nil {
		return nil, "", protocol.E("not_found", "配对码无效或已使用")
	}
	// ClaimTTL 必须在服务端生效，不能只作为 expiresInSeconds 告知客户端：
	// 过期后仍未使用的配对码必须失效，否则泄露的码可被无限期领取（B20）。
	if r.limits.ClaimTTL > 0 && time.Since(target.PairingAt) > r.limits.ClaimTTL {
		target.PairingCode = ""
		target.PairingAt = time.Time{}
		r.markDirty()
		return nil, "", protocol.E("not_found", "配对码已过期")
	}
	token, err := randomID("dev_", 24)
	if err != nil {
		return nil, "", err
	}
	target.Owner = owner
	target.PairingCode = ""
	target.ClaimedAt = time.Now().UTC()
	target.LastSeen = time.Now().UTC()
	r.markDirty()
	out := *target
	return &out, token, nil
}

// AuthenticateDevice 校验设备令牌，返回设备 ID。
func (r *Registry) AuthenticateDevice(deviceID, token string) (*Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok || d.Owner == "" {
		return nil, protocol.E("unauthorized", "设备未配对")
	}
	if !d.tokenMatches(token) {
		return nil, protocol.E("unauthorized", "设备令牌无效")
	}
	d.LastSeen = time.Now().UTC()
	out := *d
	return &out, nil
}

// SetDeviceToken 供 Claim 之后由隧道层写入令牌哈希。
// 令牌本身不落盘，只保留哈希，避免磁盘泄露长期凭据。
func (r *Registry) SetDeviceToken(deviceID, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		return protocol.E("not_found", "设备不存在")
	}
	d.TokenHash = hashToken(token)
	r.markDirty()
	return nil
}

// Owner 返回设备归属用户；未归属时返回空。
func (r *Registry) Owner(deviceID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		return "", protocol.E("not_found", "设备不存在")
	}
	return d.Owner, nil
}

// SetOnline 更新隧道在线状态。
func (r *Registry) SetOnline(deviceID string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[deviceID]; ok {
		d.Online = online
		d.LastSeen = time.Now().UTC()
	}
}

// List 返回某用户名下的设备。
func (r *Registry) List(owner string) []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Device{}
	for _, d := range r.devices {
		if d.Owner == owner {
			out = append(out, *d)
		}
	}
	return out
}

// Revoke 解除设备归属，令牌立即失效。
func (r *Registry) Revoke(owner, deviceID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok || d.Owner != owner {
		return protocol.E("not_found", "设备不存在")
	}
	delete(r.devices, deviceID)
	r.markDirty()
	return nil
}

// Stats 返回注册表规模，用于诊断。
func (r *Registry) Stats() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	online := 0
	claimed := 0
	for _, d := range r.devices {
		if d.Online {
			online++
		}
		if d.Owner != "" {
			claimed++
		}
	}
	return map[string]any{"devices": len(r.devices), "claimed": claimed, "online": online, "max": r.limits.MaxDevices}
}
