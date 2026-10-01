package transport

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PublicOrigin 是部署时显式声明的对外来源（对应 --public-origin）。
//
// 为什么需要它：默认配置只接受环回监听，此时 Host 与 Origin 天然就是监听
// 地址本身。一旦把桥放到反向代理后面，TLS 在代理处终止、回源是明文 HTTP，
// r.TLS 恒为 nil，桥无法自行推断浏览器看到的 scheme；而浏览器发来的
// Host/Origin 是代理对外的那个地址。与其去相信任意 X-Forwarded-*
// （架构 S09 明确禁止），不如让部署者写死一个来源。
//
// 零值表示未启用：只接受监听地址本身，行为与本地用法完全一致。
type PublicOrigin struct {
	Scheme string // http 或 https
	Host   string // host[:port]；与 Host / Origin 头逐字比较
}

func (o PublicOrigin) Enabled() bool { return o.Host != "" }

// String 供日志使用；未启用时返回空串。
func (o PublicOrigin) String() string {
	if !o.Enabled() {
		return ""
	}
	return o.Scheme + "://" + o.Host
}

// ParsePublicOrigin 校验并解析 --public-origin 的取值。
// 只接受「来源」本身：scheme + host[:port]，不带路径、查询串或用户信息。
func ParsePublicOrigin(raw string) (PublicOrigin, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return PublicOrigin{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return PublicOrigin{}, fmt.Errorf("--public-origin 不是合法 URL：%w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return PublicOrigin{}, errors.New("--public-origin 只接受 http 或 https")
	}
	if u.Host == "" {
		return PublicOrigin{}, errors.New("--public-origin 缺少主机名")
	}
	if u.Path != "" && u.Path != "/" {
		return PublicOrigin{}, errors.New("--public-origin 只能是来源，不能带路径")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return PublicOrigin{}, errors.New("--public-origin 不能带查询串或片段")
	}
	if u.User != nil {
		return PublicOrigin{}, errors.New("--public-origin 不能带用户信息")
	}
	return PublicOrigin{Scheme: u.Scheme, Host: u.Host}, nil
}

// hostAllowed 核对 Host 头：默认只接受监听地址本身；声明了对外来源时
// 也接受该来源的 host——反向代理默认保留原始 Host，因此浏览器发来的
// 就是对外那个地址。
func (s *Server) hostAllowed(value string) bool {
	if value == s.host {
		return true
	}
	return s.publicOrigin.Enabled() && strings.EqualFold(value, s.publicOrigin.Host)
}

// originAllowed 核对 Origin 头。scheme 不能只看 r.TLS：反代终止 TLS 时
// 它恒为 nil，据此推断会得到 http，而浏览器发的是 https。
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if origin == scheme+"://"+s.host {
		return true
	}
	return s.publicOrigin.Enabled() && origin == s.publicOrigin.String()
}

// secureCookie 决定会话 Cookie 是否带 Secure。反代终止 TLS 时 r.TLS 为
// nil，而浏览器看到的是 https；架构 S09 规定由「已配置的 HTTPS public
// origin」决定，而不是由回源连接决定。
func (s *Server) secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.publicOrigin.Enabled() && s.publicOrigin.Scheme == "https"
}

// wsOriginPatterns 让 WebSocket 层与上面的来源规则保持一致。
// coder/websocket 默认要求 Origin.Host == r.Host；反向代理若改写了 Host
// （例如 header_up Host {upstream_hostport}），合法连接会被库这一层拒掉，
// 而桥自己的检查是明确允许该来源的。列表为空表示不额外放宽。
func (s *Server) wsOriginPatterns() []string {
	if !s.publicOrigin.Enabled() {
		return nil
	}
	return []string{s.publicOrigin.String()}
}
