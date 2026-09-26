package transport

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"pi-bridge-go/internal/management"
	"pi-bridge-go/internal/observe"
	"pi-bridge-go/internal/pi"
	"pi-bridge-go/internal/presentation"
	"pi-bridge-go/internal/protocol"
	run "pi-bridge-go/internal/runtime"
	"pi-bridge-go/internal/sessions"
	"pi-bridge-go/internal/storage"
	"pi-bridge-go/internal/terminal"
	"pi-bridge-go/internal/workspace"
)

// cookieName 是浏览器换取会话 Cookie 后使用的凭据名。
const cookieName = "pi_bridge_session"

// SupportedMethods 是桥当前实现的全部命令。
// 能力清单与 main 的指标方法表都从这里取，避免两处漂移。
var SupportedMethods = []string{
	"worker.list",
	"session.start", "session.state", "session.prompt", "session.abort", "session.stop",
	"session.subscribe", "session.unsubscribe",
	"session.steer", "session.follow_up", "session.set_queue_mode",
	"session.models", "session.set_model", "session.cycle_model",
	"session.thinking_levels", "session.set_thinking", "session.cycle_thinking",
	"session.compact", "session.set_auto_compaction", "session.set_auto_retry", "session.abort_retry",
	"session.new", "session.switch", "session.fork", "session.clone",
	"session.tree", "session.fork_messages", "session.entries",
	"session.bash", "session.abort_bash", "session.bash_output",
	"session.ui_response", "session.pending_dialogs",
	"session.stats", "session.set_name", "session.last_assistant", "session.commands",
	"session.export_html",
	"sessions.search", "sessions.delete",
	"config.models", "config.models.raw", "config.models.write",
	"config.models.discover", "config.models.test", "config.catalog",
	"config.packages", "config.settings", "config.trust",
	"terminal.open", "terminal.input", "terminal.resize", "terminal.close", "terminal.list",
	"files.list", "files.index", "files.stat", "files.read", "files.roots",
	"git.status", "git.diff",
}

// Server 是 HTTP 与 WebSocket 入口，只做接入、鉴权与限额。
type Server struct {
	manager      *run.Manager
	store        *sessions.Store
	terminals    *terminal.Manager
	files        *workspace.Files
	piConfig     *management.Config
	discovery    management.DiscoveryLimits
	exportDir    string
	ui           *presentation.Renderer
	extState     *extensionState
	receipts     *storage.Receipts
	metrics      *observe.Metrics
	token, host  string
	connections  chan struct{}
	operations   chan struct{}
	tunnelBridge *TunnelBridge
}

// New 构造入口；token 至少 32 字符，host 为监听地址上的主机名。
func New(manager *run.Manager, store *sessions.Store, terminals *terminal.Manager, files *workspace.Files, piConfig *management.Config, discovery management.DiscoveryLimits, exportDir string, receipts *storage.Receipts, metrics *observe.Metrics, token, host string, ui *presentation.Renderer) *Server {
	return &Server{
		manager:     manager,
		store:       store,
		terminals:   terminals,
		files:       files,
		piConfig:    piConfig,
		discovery:   discovery,
		exportDir:   exportDir,
		ui:          ui,
		extState:    newExtensionState(64),
		receipts:    receipts,
		metrics:     metrics,
		token:       token,
		host:        host,
		connections: make(chan struct{}, 8),
		operations:  make(chan struct{}, 16),
	}
}

// SetTunnelBridge 注入隧道接入层，使云端帧能复用同一套命令分发。
func (s *Server) SetTunnelBridge(b *TunnelBridge) { s.tunnelBridge = b }

// bearer 校验 Bearer token；只接受请求头，不接收 URL 查询参数。
func (s *Server) bearer(r *http.Request) bool {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if value == r.Header.Get("Authorization") {
		return false
	}
	got := sha256.Sum256([]byte(value))
	want := sha256.Sum256([]byte(s.token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// signature 用 HMAC 生成带过期时间的 Cookie 值，避免依赖服务端会话存储。
func (s *Server) signature(exp string) string {
	m := hmac.New(sha256.New, []byte(s.token))
	m.Write([]byte(exp + "|" + s.host))
	return hex.EncodeToString(m.Sum(nil))
}

// authorized 同时接受 Bearer token 与未过期的会话 Cookie。
func (s *Server) authorized(r *http.Request) bool {
	if s.bearer(r) {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return false
	}
	return hmac.Equal([]byte(parts[1]), []byte(s.signature(parts[0])))
}

// ServeHTTP 统一做 Host、Origin、鉴权与限额检查，再分发到具体端点。
// handleUiResponse 处理扩展对话回执。
//
// 表单提交（application/x-www-form-urlencoded）或 JSON 都可以。
// 三种语义互斥，优先级 cancelled > confirmed > value，
// 与 Pi 的 parseResponse 一致。
func (s *Server) handleUiResponse(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ui/sessions/"), "/ui-response")
	sessionID := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		sessionID = rest[:i]
	}
	if !sessions.ValidID(sessionID) {
		writeError(w, 400, protocol.E("invalid_params", "会话 ID 不合法"))
		return
	}

	var p struct {
		ID        string `json:"id" form:"id"`
		Value     string `json:"value" form:"value"`
		Confirmed string `json:"confirmed" form:"confirmed"`
		Cancelled string `json:"cancelled" form:"cancelled"`
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
			writeError(w, 400, protocol.E("invalid_params", "请求体不是合法 JSON"))
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeError(w, 400, protocol.E("invalid_params", "表单解析失败"))
			return
		}
		p.ID = r.FormValue("id")
		p.Value = r.FormValue("value")
		p.Confirmed = r.FormValue("confirmed")
		p.Cancelled = r.FormValue("cancelled")
	}
	if p.ID == "" {
		writeError(w, 400, protocol.E("invalid_params", "缺少对话 id"))
		return
	}

	wkr, err := s.manager.Get(sessionID)
	if err != nil {
		writeError(w, 400, protocol.E("worker_not_running", "会话没有活跃的工作进程"))
		return
	}
	var value *string
	var confirmed *bool
	cancelled := p.Cancelled == "true"
	if !cancelled && p.Confirmed == "true" {
		v := true
		confirmed = &v
	} else if !cancelled {
		v := p.Value
		value = &v
	}
	if err := wkr.UIResponse(r.Context(), p.ID, value, confirmed, cancelled); err != nil {
		writeError(w, 400, err)
		return
	}
	// htmx 默认会把响应换入目标；回执成功无需换任何内容。
	w.WriteHeader(204)
}

// serveUI 处理 UI 层请求：外壳、静态资源与 htmx 片段。
// 返回 true 表示已处理，调用方应直接返回。
//
// 与 JSON API 的分工：
//   - 这里返回 HTML 片段，htmx 直接换入 DOM
//   - 流式对话不走这里，走 WS（见 wsHandler）
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) bool {
	if s.ui == nil {
		// 未配置 UI 包时 UI 路由整体不存在，回 404 让调用方继续。
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/ui/") {
			writeError(w, 404, protocol.E("not_found", "未配置 UI 包，使用 --ui-dir 指定 pi-webui-htmx 目录"))
			return true
		}
		return false
	}

	path := r.URL.Path
	switch {
	case path == "/":
		id := r.URL.Query().Get("session")
		if id != "" && !sessions.ValidID(id) {
			writeError(w, 400, protocol.E("invalid_params", "会话 ID 不合法"))
			return true
		}
		html, err := s.ui.RenderShell(id)
		if err != nil {
			writeError(w, 500, err)
			return true
		}
		writeHTML(w, html)
		return true

	case strings.HasPrefix(path, "/assets/"):
		name := strings.TrimPrefix(path, "/assets/")
		encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
		body, mime, ok := s.ui.Asset(name, encoding)
		if !ok {
			writeError(w, 404, protocol.E("not_found", "资源不存在"))
			return true
		}
		// 文件名带内容哈希，可长期不可变缓存。
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", mime)
		if encoding != "" {
			// Vary 必须声明，否则共享缓存会把压缩版发给不接受编码的客户端。
			w.Header().Set("Content-Encoding", encoding)
			w.Header().Set("Vary", "Accept-Encoding")
		}
		w.WriteHeader(200)
		_, _ = w.Write(body)
		return true

	case path == "/ui/sessions":
		offset, err := number(r, "offset", 0)
		if err != nil {
			writeError(w, 400, err)
			return true
		}
		limit, err := number(r, "limit", 50)
		if err != nil {
			writeError(w, 400, err)
			return true
		}
		list, lerr := s.store.List(r.Context(), offset, limit)
		if lerr != nil {
			writeError(w, 400, lerr)
			return true
		}
		html, rerr := s.ui.RenderSessionsPage(list, r.URL.Query().Get("selected"), offset)
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case strings.HasPrefix(path, "/ui/sessions/") && strings.HasSuffix(path, "/history"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/ui/sessions/"), "/history")
		if !sessions.ValidID(id) {
			writeError(w, 400, protocol.E("invalid_params", "会话 ID 不合法"))
			return true
		}
		before := r.URL.Query().Get("before")
		leaf := r.URL.Query().Get("leafId")
		// 滚动模式由桥下发，模板不写滚动逻辑。
		// prepend：内容加在视口上方，保持离底部距离；
		// append：首屏或翻到底，滚到新片段。
		// 先设响应头再处理，失败时前端也能看到模式。
		if before != "" || leaf != "" {
			w.Header().Set("X-Scroll-Mode", "prepend")
		} else {
			w.Header().Set("X-Scroll-Mode", "append")
		}
		page, perr := s.store.History(r.Context(), id, leaf, before, 50)
		if perr != nil {
			writeError(w, 400, perr)
			return true
		}
		html, herr := s.ui.RenderHistory(id, page)
		if herr != nil {
			writeError(w, 500, herr)
			return true
		}
		writeHTML(w, html)
		return true

	case path == "/ui/models":
		out, merr := s.piConfig.Models()
		if merr != nil {
			writeError(w, 400, merr)
			return true
		}
		models := presentation.ConfigModels(out)
		html, rerr := s.ui.RenderModels(models, "")
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case path == "/ui/diff":
		diff, truncated, err := s.files.GitDiff(r.Context(), r.URL.Query().Get("path"), r.URL.Query().Get("staged") == "true", 512<<10)
		if err != nil {
			writeError(w, 400, err)
			return true
		}
		html, err := s.ui.RenderDiff("", presentation.ParseDiff(diff))
		if err != nil {
			writeError(w, 500, err)
			return true
		}
		if truncated {
			html += "<p class=\"empty-note\">差异已达到预览上限。</p>"
		}
		writeHTML(w, html)
		return true

	case path == "/ui/packages":
		pkgs, perr := s.piConfig.Packages(r.Context(), s.discovery)
		if perr != nil {
			writeError(w, 400, perr)
			return true
		}
		html, rerr := s.ui.RenderPackages(toAnyMaps(pkgs))
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case path == "/ui/files":
		root := r.URL.Query().Get("path")
		if root == "" {
			roots := s.files.Roots()
			if len(roots) > 0 {
				root = roots[0]
			}
		}
		entries, truncated, ferr := s.files.List(root)
		if ferr != nil {
			writeError(w, 400, ferr)
			return true
		}
		html, rerr := s.ui.RenderFiles(root, toAnyMaps(entries), truncated)
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case path == "/ui/extensions/status":
		html, rerr := s.ui.RenderExtensionStatus(s.extensionStatuses())
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case strings.HasPrefix(path, "/ui/extensions/dialog/"):
		// GET：渲染某个待回复对话。对话不存在时返回 204，
		// 让 htmx 移除占位而不是显示错误。
		id := strings.TrimPrefix(path, "/ui/extensions/dialog/")
		if id == "" || strings.ContainsAny(id, "/\\") {
			writeError(w, 400, protocol.E("invalid_params", "对话 ID 不合法"))
			return true
		}
		raw, found := s.pendingDialog(id)
		if !found {
			w.WriteHeader(204)
			return true
		}
		d, derr := presentation.DialogFromPi(id, r.URL.Query().Get("sessionId"), raw)
		if derr != nil {
			writeError(w, 400, derr)
			return true
		}
		html, rerr := s.ui.RenderExtensionDialog(d)
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true

	case path == "/ui/extensions/dialogs":
		// 当前会话全部待回复对话，供页面加载时恢复。
		sessionID := r.URL.Query().Get("sessionId")
		items, ferr := s.pendingDialogsFor(sessionID)
		if ferr != nil {
			writeError(w, 400, ferr)
			return true
		}
		dialogs := make([]presentation.DialogData, 0, len(items))
		for _, raw := range items {
			d, derr := presentation.DialogFromPi(dialogID(raw), sessionID, raw)
			if derr != nil {
				continue
			}
			dialogs = append(dialogs, d)
		}
		html, rerr := s.ui.RenderExtensionDialogs(dialogs)
		if rerr != nil {
			writeError(w, 500, rerr)
			return true
		}
		writeHTML(w, html)
		return true
	}
	return false
}

// pendingDialog 在受管 worker 中查找待回复对话。
func (s *Server) pendingDialog(id string) (json.RawMessage, bool) {
	for _, info := range s.manager.List() {
		w, err := s.manager.Get(info.SessionID)
		if err != nil {
			continue
		}
		if raw, found := w.PendingDialog(id); found {
			return raw, true
		}
	}
	return nil, false
}

// pendingDialogsFor 返回某会话的全部待回复对话。
func (s *Server) pendingDialogsFor(sessionID string) ([]json.RawMessage, error) {
	if sessionID == "" {
		return nil, protocol.E("invalid_params", "缺少 sessionId")
	}
	w, err := s.manager.Get(sessionID)
	if err != nil {
		return nil, nil // 没有活 worker 就没有待回复对话，不是错误。
	}
	return w.PendingDialogPayloads(), nil
}

// extensionStatuses 汇总所有活跃 worker 的扩展状态行。
//
// 当前实现：状态来自 WS 推送的 setStatus，由客户端直接更新 DOM；
// 这里只在页面初次加载时给一份快照。刷新后状态会从空开始，
// 直到插件再次 setStatus——这是已知限制，不做假持久化。
func (s *Server) extensionStatuses() []presentation.StatusItem {
	out := []presentation.StatusItem{}
	for _, raw := range s.extState.snapshot() {
		out = append(out, presentation.StatusItem{Key: raw.key, Text: raw.text})
	}
	return out
}

// dialogID 从 extension_ui_request 载荷里取 id。
func dialogID(raw json.RawMessage) string {
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.ID
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 一次性协商编码，写响应的辅助函数从内部键读取后即删。
	// 走内部键而不是改十几个调用点的签名：那些函数拿不到 *http.Request。
	if encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding")); encoding != "" {
		w.Header().Set(encodingKey, encoding)
	}
	if r.Host != s.host {
		writeError(w, http.StatusForbidden, protocol.E("host_denied", "Host 不在预期范围内"))
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != scheme+"://"+s.host {
		writeError(w, http.StatusForbidden, protocol.E("origin_denied", "未启用跨源访问"))
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth" {
		if !s.bearer(r) {
			writeError(w, 401, protocol.E("unauthorized", "需要 Bearer token"))
			return
		}
		expires := time.Now().Add(8 * time.Hour)
		exp := strconv.FormatInt(expires.Unix(), 10)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: exp + "." + s.signature(exp), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Path: "/", Expires: expires, MaxAge: 8 * 60 * 60})
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	// 登录外壳和哈希资源不含用户数据；所有片段与 API 仍需认证。
	if r.Method == http.MethodGet && (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/")) {
		if s.serveUI(w, r) {
			return
		}
	}
	if !s.authorized(r) {
		s.metrics.AuthFailure()
		writeError(w, 401, protocol.E("unauthorized", "需要身份验证"))
		return
	}
	// 扩展对话回执是 POST，且要转成 WS 命令 session.ui_response。
	// 必须在通用的「只接受 GET」之前处理。
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/ui/sessions/") && strings.HasSuffix(r.URL.Path, "/ui-response") {
		if s.ui == nil {
			writeError(w, 404, protocol.E("not_found", "未配置 UI 包"))
			return
		}
		s.handleUiResponse(w, r)
		return
	}

	if r.Method != http.MethodGet {
		writeError(w, 405, protocol.E("invalid_request", "请求方法不被允许"))
		return
	}
	// 导出下载是桥自身能力，不依赖 UI 包：没配 --ui-dir 时也要能取回文件。
	if strings.HasPrefix(r.URL.Path, "/ui/exports/") {
		s.serveExport(w, r)
		return
	}
	// ---- UI 层：htmx 片段由桥渲染，静态资源来自 UI 包构建产物 ----
	if s.serveUI(w, r) {
		return
	}
	switch r.URL.Path {
	case "/api/v1/capabilities":
		writeJSON(w, 200, map[string]any{
			"version": 1, "phase": "A", "piBaseline": "0.85.1",
			"methods":          SupportedMethods,
			"replay":           true,
			"persistentDedup":  true,
			"extensionDialogs": "interactive",
			"relay":            s.tunnelBridge != nil,
			"history":          "v3-disk-branch",
			"exportDir":        s.exportDir,
			"limits": map[string]int{
				"connections": 8, "inFlightOperations": 16,
				"wsRequestBytes": 1 << 20, "wsResponseBytes": 512 << 10,
				"connectionQueueBytes": 1 << 20, "requestIdsPerConnection": 1024,
				"replayItems": 256, "replayBytes": 1 << 20,
				"terminals": 4, "terminalIdleSeconds": 600,
			},
		})
	case "/api/v1/sessions":
		limit, err := number(r, "limit", 50)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		offset, err := number(r, "offset", 0)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		list, err := s.store.List(r.Context(), offset, limit)
		respond(w, list, err)
	case "/api/v1/metrics":
		sessionStats := s.store.Index().Stats()
		if n, ok := sessionStats["sessions"].(int); ok {
			s.metrics.SetSessionsIndexed(n)
		}
		out := map[string]any{"metrics": s.metrics.Snapshot(), "sessions": sessionStats, "receipts": s.receipts.Stats(), "workers": s.manager.List(), "terminals": s.terminals.List()}
		if s.tunnelBridge != nil {
			out["tunnel"] = s.tunnelBridge.Stats()
		}
		writeJSON(w, 200, out)
	case "/api/v1/ws":
		s.serveWS(w, r)
	default:
		prefix := "/api/v1/sessions/"
		suffix := "/history"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
			limit, err := number(r, "limit", 50)
			if err != nil {
				writeError(w, 400, err)
				return
			}
			s.metrics.HistoryRequest()
			page, err := s.store.History(r.Context(), id, r.URL.Query().Get("leafId"), r.URL.Query().Get("before"), limit)
			respond(w, page, err)
			return
		}
		writeError(w, 404, protocol.E("not_found", "接口不存在"))
	}
}

// safeExportName 限制导出文件名，防止借导出路径写到别处。
// 只允许安全字符，且必须以 .html 结尾。
func safeExportName(name string) (string, error) {
	if name == "" {
		return "session.html", nil
	}
	if len(name) > 128 {
		return "", protocol.E("invalid_params", "文件名过长")
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return "", protocol.E("invalid_params", "文件名只能包含字母、数字、-、_ 和 .")
		}
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, ".") {
		return "", protocol.E("invalid_params", "文件名不合法")
	}
	if !strings.HasSuffix(name, ".html") {
		return "", protocol.E("invalid_params", "导出文件必须以 .html 结尾")
	}
	return name, nil
}

// number 解析分页用数字参数。
func number(r *http.Request, key string, fallback int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, protocol.E("invalid_params", "数字查询参数无效")
	}
	return n, nil
}

// writeJSON 输出 JSON 响应。
// toAnyMaps 把任意结构体切片转成 []map[string]any，
// 让呈现层用统一的字段读取方式，不必为每种返回类型写转换。
func toAnyMaps(v any) []map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// writeHTML 输出 HTML 片段。htmx 靠 Content-Type 决定如何处理响应。
func writeHTML(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")
	encoding := takeEncoding(w)
	body := []byte(html)
	// 必须与 ShouldCompress 一致：Compress 在 body 小于阈值时直接写原文，
	// 这里若仍然标 Content-Encoding，客户端会按该编码解压明文并失败。
	// 浏览器表现为 fetch 直接 reject（"Failed to fetch"），任何小于 1 KB
	// 的 HTML 片段——历史分页、扩展对话框、包清单——全都换不进去。
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.WriteHeader(200)
	_, _ = presentation.Compress(w, body, encoding)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		body = []byte(`{"error":"encode_failed"}`)
		status = 500
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Vary", "Accept-Encoding")
	encoding := takeEncoding(w)
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.WriteHeader(status)
	_, _ = presentation.Compress(w, body, encoding)
}

// encodingKey 是 ServeHTTP 与写响应辅助函数之间传递协商结果的内部键。
// 用 Header 承载只为省去改十几个调用点签名；读取后立即删除，不会外泄。
const encodingKey = "X-Pi-Bridge-Encoding"

// takeEncoding 取出协商到的编码并清除内部键。
func takeEncoding(w http.ResponseWriter) string {
	encoding := w.Header().Get(encodingKey)
	w.Header().Del(encodingKey)
	return encoding
}

// writeError 把内部错误转成协议错误响应。
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, protocol.Reply("", nil, err))
}

// respond 按错误码映射 HTTP 状态；未识别的错误一律按 500 处理。
func respond(w http.ResponseWriter, data any, err error) {
	if err == nil {
		writeJSON(w, 200, data)
		return
	}
	status := 500
	var pe *protocol.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "invalid_params", "invalid_history":
			status = 400
		case "forbidden":
			status = 403
		case "not_found":
			status = 404
		case "conflict", "busy":
			status = 409
		case "unsupported_version":
			status = 422
		case "limit_exceeded":
			status = 413
		}
	}
	writeError(w, status, err)
}

// connection 是单条 WebSocket 连接的发送队列、订阅与命令信号量。
type connection struct {
	server         *Server
	ws             *websocket.Conn
	ctx            context.Context
	cancel         context.CancelFunc
	out            chan []byte
	queued         atomic.Int64
	mu             sync.Mutex
	subs           map[string]*run.Subscription
	termSubs       map[string]*terminal.Subscription
	normal, urgent chan struct{}
}

// trackTerminal 登记终端订阅，连接关闭时统一解除。
func (c *connection) trackTerminal(id string, sub *terminal.Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.termSubs == nil {
		c.termSubs = map[string]*terminal.Subscription{}
	}
	if old, ok := c.termSubs[id]; ok {
		old.Close()
	}
	c.termSubs[id] = sub
}

// dropTerminal 解除并移除终端订阅。
func (c *connection) dropTerminal(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sub, ok := c.termSubs[id]; ok {
		sub.Close()
		delete(c.termSubs, id)
	}
}

// send 把消息放入发送队列；单条或队列总量超限即判定该连接异常并关闭，
// 绝不因为某个慢客户端拖住 Pi 输出或让队列无界增长。
// sendRaw 发送已序列化的帧，用于补发。
func (c *connection) sendRaw(b []byte) bool {
	if len(b) == 0 || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	if c.queued.Add(int64(len(b))) > 1<<20 {
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		c.queued.Add(-int64(len(b)))
		return false
	default:
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
}

func (c *connection) send(m protocol.Message) bool {
	b, err := json.Marshal(m)
	if err != nil || len(b) > 512<<10 {
		c.cancel()
		return false
	}
	if c.queued.Add(int64(len(b))) > 1<<20 {
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		c.queued.Add(-int64(len(b)))
		return false
	default:
		c.queued.Add(-int64(len(b)))
		c.cancel()
		return false
	}
}

// writer 是连接内唯一的写协程，保证 WebSocket 写入串行化。
func (c *connection) writer() {
	defer c.cancel()
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			c.queued.Add(-int64(len(b)))
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// serveWS 升级连接，随后串行读取命令、异步执行，读循环永不被命令阻塞。
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	select {
	case s.connections <- struct{}{}:
		defer func() { <-s.connections }()
	default:
		writeError(w, 429, protocol.E("limit_exceeded", "连接数量已达上限"))
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(s.manager.Context())
	defer cancel()
	c := &connection{server: s, ws: ws, ctx: ctx, cancel: cancel, out: make(chan []byte, 32), subs: map[string]*run.Subscription{}, termSubs: map[string]*terminal.Subscription{}, normal: make(chan struct{}, 8), urgent: make(chan struct{}, 2)}
	go c.writer()
	defer func() {
		c.mu.Lock()
		for id, sub := range c.subs {
			delete(c.subs, id)
			sub.Close()
		}
		for id, sub := range c.termSubs {
			delete(c.termSubs, id)
			sub.Close()
		}
		c.mu.Unlock()
	}()
	seen := map[string]bool{}
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var req protocol.Request
		if typ != websocket.MessageText || protocol.Decode(b, &req) != nil {
			c.send(protocol.Reply("", nil, protocol.E("invalid_request", "需要一条 JSON 命令")))
			continue
		}
		if req.Version != 1 {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("unsupported_version", "仅支持版本 1")))
			continue
		}
		if req.Kind != "command" || req.RequestID == "" || len(req.RequestID) > 128 {
			c.send(protocol.Reply("", nil, protocol.E("invalid_request", "命令必须带长度受限的 requestId")))
			continue
		}
		// 跨重启去重：同一 requestId 已执行过就直接回放结论，绝不重新执行。
		if rec, ok := s.receipts.Lookup(req.RequestID); ok && rec.Outcome != storage.OutcomeRejected {
			c.send(protocol.Reply(req.RequestID, map[string]any{
				"duplicate": true,
				"outcome":   string(rec.Outcome),
				"method":    rec.Method,
				"at":        rec.At.UTC().Format(time.RFC3339Nano),
			}, nil))
			continue
		}
		if seen[req.RequestID] {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("conflict", "requestId 已被使用，有副作用的命令请勿重试")))
			continue
		}
		if len(seen) >= 1024 {
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("limit_exceeded", "请用新的 requestId 重新连接")))
			return
		}
		seen[req.RequestID] = true
		urgent := req.Method == "session.abort" || req.Method == "session.stop"
		sem := c.normal
		if urgent {
			sem = c.urgent
		}
		select {
		case sem <- struct{}{}:
		default:
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("busy", "该连接的在途命令数已达上限")))
			continue
		}
		if !urgent {
			select {
			case s.operations <- struct{}{}:
			default:
				<-sem
				c.send(protocol.Reply(req.RequestID, nil, protocol.E("busy", "桥的在途命令数已达上限")))
				continue
			}
		}
		go func(req protocol.Request, sem chan struct{}, urgent bool) {
			defer func() {
				<-sem
				if !urgent {
					<-s.operations
				}
			}()
			// 命令寿命有意长于浏览器连接：断开只停止等待，不取消已接受的任务。
			opctx, stop := context.WithTimeout(s.manager.Context(), s.manager.Timeout())
			defer stop()
			s.metrics.CommandStarted(req.Method)
			data, err := c.dispatch(opctx, req)
			if err != nil {
				s.metrics.CommandFailed(req.Method, errorCodeOf(err))
			}
			c.send(protocol.Reply(req.RequestID, data, err))
			s.recordReceipt(req, err)
		}(req, sem, urgent)
	}
}

// errorCodeOf 取协议错误码，未知错误归一化。
func errorCodeOf(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return "internal"
}

// notExecutedCodes 是「命令从未送达 Pi」的错误码集合。
// 这些失败允许客户端用同一 requestId 重试，因此回执记为 rejected，
// 不会在后续连接里被当成重复执行而挡住合法重试。
var notExecutedCodes = map[string]struct{}{
	"invalid_request": {}, "invalid_params": {}, "unsupported_method": {},
	"unsupported_version": {}, "busy": {}, "limit_exceeded": {},
	"resync_required": {}, "unauthorized": {}, "host_denied": {},
	"origin_denied": {},
}

// recordReceipt 落一条命令回执，供跨重启去重与对账。
// 写失败不影响命令结果。
func (s *Server) recordReceipt(req protocol.Request, err error) {
	if s.receipts == nil {
		return
	}
	outcome := storage.OutcomeOK
	if err != nil {
		code := errorCodeOf(err)
		if _, skip := notExecutedCodes[code]; skip {
			outcome = storage.OutcomeRejected
		} else if code == "outcome_unknown" {
			outcome = storage.OutcomeUnknown
		} else {
			outcome = storage.OutcomeError
		}
	}
	_ = s.receipts.Record(storage.Receipt{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Method:    req.Method,
		Outcome:   outcome,
	})
}

// connSink 是连接相关的少量能力：生命周期上下文、发送帧、登记终端订阅。
// WebSocket 连接与隧道虚拟连接各自实现它，从而共用同一份命令分发。
type connSink interface {
	connContext() context.Context
	send(m protocol.Message) bool
	trackTerminal(id string, sub *terminal.Subscription)
	dropTerminal(id string)
}

// dispatchCommon 执行除订阅以外的命令。
// WebSocket 连接与隧道虚拟连接共用这一份实现，避免两处逻辑漂移。
func (s *Server) dispatchCommon(ctx context.Context, r protocol.Request, sink connSink) (any, error) {
	empty := func() error { return protocol.Decode(r.Params, &struct{}{}) }
	switch r.Method {
	case "worker.list":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.manager.List(), nil
	case "session.start":
		var p struct {
			Cwd string `json:"cwd"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		w, err := s.manager.Start(ctx, r.SessionID, p.Cwd)
		if err != nil {
			return nil, err
		}
		return w.Info(), nil
	}
	// 文件、配置、终端和磁盘会话操作独立于 Pi 生命周期。
	// 只有 session.* 命令要求显式启动过的工作进程。
	var w *run.Worker
	if strings.HasPrefix(r.Method, "session.") {
		var err error
		w, err = s.manager.Get(r.SessionID)
		if err != nil {
			return nil, err
		}
	}
	switch r.Method {
	case "session.state":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.State(ctx)
	case "session.models":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Models(ctx)
	case "session.set_model":
		var p struct {
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.SetModel(ctx, p.Provider, p.ModelID)
	case "session.cycle_model":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.CycleModel(ctx)
	case "session.thinking_levels":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.ThinkingLevels(ctx)
	case "session.set_thinking":
		var p struct {
			Level string `json:"level"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetThinkingLevel(ctx, p.Level); err != nil {
			return nil, err
		}
		return map[string]any{"level": p.Level}, nil
	case "session.cycle_thinking":
		if err := empty(); err != nil {
			return nil, err
		}
		level, err := w.CycleThinkingLevel(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"level": level}, nil
	case "session.set_queue_mode":
		var p struct {
			Kind string `json:"kind"`
			Mode string `json:"mode"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetQueueMode(ctx, p.Kind, p.Mode); err != nil {
			return nil, err
		}
		return map[string]any{"kind": p.Kind, "mode": p.Mode}, nil
	case "session.steer":
		var p struct {
			Text   string     `json:"text"`
			Images []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.Steer(ctx, p.Text, images); err != nil {
			return nil, err
		}
		return map[string]bool{"queued": true}, nil
	case "session.follow_up":
		var p struct {
			Text   string     `json:"text"`
			Images []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.FollowUp(ctx, p.Text, images); err != nil {
			return nil, err
		}
		return map[string]bool{"queued": true}, nil
	case "session.compact":
		var p struct {
			Instructions string `json:"customInstructions"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Compact(ctx, p.Instructions)
	case "session.set_auto_compaction":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoCompaction(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": p.Enabled}, nil
	case "session.set_auto_retry":
		var p struct {
			Enabled bool `json:"enabled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetAutoRetry(ctx, p.Enabled); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": p.Enabled}, nil
	case "session.abort_retry":
		if err := empty(); err != nil {
			return nil, err
		}
		if err := w.AbortRetry(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"aborted": true}, nil
	case "session.stats":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Stats(ctx)
	case "session.set_name":
		var p struct {
			Name string `json:"name"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.SetName(ctx, p.Name); err != nil {
			return nil, err
		}
		return map[string]bool{"renamed": true}, nil
	case "session.last_assistant":
		if err := empty(); err != nil {
			return nil, err
		}
		text, err := w.LastAssistantText(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	case "session.commands":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Commands(ctx)
	case "session.tree":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.Tree(ctx)
	case "session.fork_messages":
		if err := empty(); err != nil {
			return nil, err
		}
		return w.ForkMessages(ctx)
	case "session.entries":
		var p struct {
			Since string `json:"since"`
			Limit int    `json:"limit"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		return w.Entries(ctx, p.Since, p.Limit)
	case "session.new":
		var p struct {
			ParentSession string `json:"parentSession"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.NewSession(ctx, p.ParentSession)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "session.switch":
		var p struct {
			SessionPath string `json:"sessionPath"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		id, err := w.SwitchSession(ctx, p.SessionPath)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "session.fork":
		var p struct {
			EntryID string `json:"entryId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return w.Fork(ctx, p.EntryID)
	case "session.clone":
		if err := empty(); err != nil {
			return nil, err
		}
		id, err := w.Clone(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessionId": id}, nil
	case "sessions.search":
		var p struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Limit <= 0 {
			p.Limit = 50
		}
		limits := sessions.DefaultSearchLimits()
		limits.MaxMatches = p.Limit
		return s.store.Search(ctx, p.Query, limits)
	case "sessions.delete":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.store.Delete(ctx, p.SessionID)
	case "session.export_html":
		var p struct {
			FileName string `json:"fileName"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		name, err := safeExportName(p.FileName)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(s.exportDir, name)
		worker, err := s.manager.Get(r.SessionID)
		if err != nil {
			return nil, err
		}
		path, err := worker.ExportHTML(ctx, target)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(path) != filepath.Clean(target) {
			return nil, protocol.E("conflict", "导出路径与请求不一致")
		}
		return map[string]any{"path": path}, nil
	case "config.models":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.piConfig.Models()
	case "config.models.raw":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.piConfig.Raw()
	case "config.models.write":
		var p struct {
			Config map[string]any `json:"config"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := s.piConfig.WriteModels(p.Config); err != nil {
			return nil, err
		}
		return map[string]bool{"written": true}, nil
	case "config.models.discover":
		var p struct {
			BaseURL string            `json:"baseUrl"`
			API     string            `json:"api"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		models, err := s.piConfig.Discover(ctx, p.BaseURL, p.API, p.APIKey, p.Headers, s.discovery)
		if err != nil {
			return nil, err
		}
		return map[string]any{"models": models}, nil
	case "config.models.test":
		var p struct {
			BaseURL string            `json:"baseUrl"`
			API     string            `json:"api"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.piConfig.TestConnection(ctx, p.BaseURL, p.API, p.APIKey, p.Headers, s.discovery)
	case "config.catalog":
		if err := empty(); err != nil {
			return nil, err
		}
		entries, err := s.piConfig.Catalog(ctx, s.discovery)
		if err != nil {
			return nil, err
		}
		return map[string]any{"models": entries}, nil
	case "config.packages":
		if err := empty(); err != nil {
			return nil, err
		}
		list, err := s.piConfig.Packages(ctx, s.discovery)
		if err != nil {
			return nil, err
		}
		return map[string]any{"packages": list}, nil
	case "config.settings":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.piConfig.Settings()
	case "config.trust":
		if err := empty(); err != nil {
			return nil, err
		}
		return s.piConfig.Trust()
	case "session.bash":
		var p struct {
			Command            string `json:"command"`
			ExcludeFromContext bool   `json:"excludeFromContext"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		result, err := w.Bash(ctx, r.RequestID, p.Command, p.ExcludeFromContext)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "session.abort_bash":
		if err := empty(); err != nil {
			return nil, err
		}
		if err := w.AbortBash(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"aborted": true}, nil
	case "session.ui_response":
		var p struct {
			ID        string  `json:"id"`
			Value     *string `json:"value"`
			Confirmed *bool   `json:"confirmed"`
			Cancelled bool    `json:"cancelled"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if err := w.UIResponse(ctx, p.ID, p.Value, p.Confirmed, p.Cancelled); err != nil {
			return nil, err
		}
		return map[string]bool{"answered": true}, nil
	case "session.pending_dialogs":
		if err := empty(); err != nil {
			return nil, err
		}
		// 返回完整载荷而不只是 ID：HTTP 端点要渲染对话框，
		// 需要 title/options/message 等字段。
		payloads := w.PendingDialogPayloads()
		items := make([]json.RawMessage, 0, len(payloads))
		items = append(items, payloads...)
		return map[string]any{"dialogs": items, "ids": w.PendingDialogs()}, nil
	case "terminal.open":
		var p struct {
			Cwd   string `json:"cwd"`
			Shell string `json:"shell"`
			Cols  uint16 `json:"cols"`
			Rows  uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.Cwd == "" {
			return nil, protocol.E("invalid_params", "cwd 不能为空")
		}
		directory, err := s.files.Stat(p.Cwd)
		if err != nil {
			return nil, err
		}
		if !directory.IsDir {
			return nil, protocol.E("invalid_params", "终端工作路径必须是目录")
		}
		term, err := s.terminals.Open(p.Cwd, p.Shell, p.Cols, p.Rows)
		if err != nil {
			return nil, err
		}
		info := term.Info()
		sub, err := term.Subscribe(64, 1<<20)
		if err != nil {
			_ = term.Close(true)
			return nil, protocol.E("worker_exited", "终端已关闭")
		}
		sink.trackTerminal(term.ID(), sub)
		connCtx := sink.connContext()
		go func() {
			defer sub.Close()
			for {
				chunk, err := sub.Next(connCtx)
				if err != nil {
					if connCtx.Err() == nil {
						sink.send(protocol.Message{Version: 1, Kind: "control", Event: "bridge.terminal_closed", Data: map[string]any{"terminalId": term.ID()}})
					}
					return
				}
				if !sink.send(protocol.Message{Version: 1, Kind: "event", Event: "terminal.output", Data: map[string]any{"terminalId": term.ID(), "data": string(chunk)}}) {
					return
				}
			}
		}()
		return map[string]any{"terminalId": info.ID, "cwd": info.Cwd, "pid": info.PID, "cols": info.Cols, "rows": info.Rows}, nil
	case "terminal.input":
		var p struct {
			TerminalID string `json:"terminalId"`
			Data       string `json:"data"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Write([]byte(p.Data)); err != nil {
			return nil, err
		}
		return map[string]bool{"written": true}, nil
	case "terminal.resize":
		var p struct {
			TerminalID string `json:"terminalId"`
			Cols       uint16 `json:"cols"`
			Rows       uint16 `json:"rows"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		term, err := s.terminals.Get(p.TerminalID)
		if err != nil {
			return nil, err
		}
		if err := term.Resize(p.Cols, p.Rows); err != nil {
			return nil, err
		}
		return map[string]bool{"resized": true}, nil
	case "terminal.close":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		sink.dropTerminal(p.TerminalID)
		if err := s.terminals.CloseTerminal(p.TerminalID); err != nil {
			return nil, err
		}
		return map[string]bool{"closed": true}, nil
	case "terminal.list":
		if err := empty(); err != nil {
			return nil, err
		}
		return map[string]any{"terminals": s.terminals.List()}, nil
	case "files.list":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		entries, truncated, err := s.files.List(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"entries": entries, "truncated": truncated}, nil
	case "files.stat":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.Stat(p.Path)
	case "files.read":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, size, err := s.files.Read(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text, "truncated": truncated, "size": size}, nil
	case "files.index":
		var p struct {
			Path  string `json:"path"`
			Query string `json:"query"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		result, err := s.files.Index(ctx, p.Path, p.Query)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "files.roots":
		if err := empty(); err != nil {
			return nil, err
		}
		return map[string]any{"roots": s.files.Roots()}, nil
	case "git.status":
		var p struct {
			Path string `json:"path"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		return s.files.GitStatus(ctx, p.Path)
	case "git.diff":
		var p struct {
			Path     string `json:"path"`
			Staged   bool   `json:"staged"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.MaxBytes == 0 {
			p.MaxBytes = 512 << 10
		}
		text, truncated, err := s.files.GitDiff(ctx, p.Path, p.Staged, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"diff": text, "truncated": truncated}, nil
	case "session.bash_output":
		var p struct {
			Path     string `json:"path"`
			MaxBytes int    `json:"maxBytes"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		text, truncated, err := w.ReadBashOutput(ctx, p.Path, p.MaxBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text, "truncated": truncated}, nil
	case "session.prompt":
		var p struct {
			Text     string     `json:"text"`
			Behavior string     `json:"streamingBehavior"`
			Images   []pi.Image `json:"images"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		images, err := pi.DecodeImages(p.Images)
		if err != nil {
			return nil, err
		}
		if err := w.Prompt(ctx, p.Text, p.Behavior, images); err != nil {
			return nil, err
		}
		return map[string]bool{"accepted": true}, nil
	case "session.abort":
		if err := empty(); err != nil {
			return nil, err
		}
		q, err := w.Abort(ctx)
		return map[string]any{"clearedQueue": q}, err
	case "session.stop":
		var p struct {
			Force bool `json:"force"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		err := w.Stop(p.Force)
		return map[string]bool{"stopped": err == nil}, err
	default:
		return nil, protocol.E("unsupported_method", "A 阶段未实现该方法")
	}
}

// subscribe 处理事件订阅：先做游标补发，再注册有界队列并持续推送。
func (c *connection) subscribe(ctx context.Context, r protocol.Request) (any, error) {
	var p struct {
		Epoch    string  `json:"epoch"`
		AfterSeq *uint64 `json:"afterSeq"`
	}
	if err := protocol.Decode(r.Params, &p); err != nil {
		return nil, err
	}
	w, err := c.server.manager.Get(r.SessionID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return nil, protocol.E("conflict", "连接已关闭")
	}
	// 带游标订阅时先补发，补不上就明确要求重新同步，不伪造无损恢复。
	if p.Epoch != "" || p.AfterSeq != nil {
		after := uint64(0)
		if p.AfterSeq != nil {
			after = *p.AfterSeq
		}
		items, ok := w.Replay(p.Epoch, after)
		if !ok {
			c.server.metrics.ReplayMiss()
			return nil, protocol.E("resync_required", "事件游标已失效，请重新读取持久历史后再订阅")
		}
		if len(items) > 0 {
			c.server.metrics.ReplayHit()
		}
		for _, item := range items {
			if !c.sendRaw(item.Payload) {
				return nil, protocol.E("conflict", "连接已关闭")
			}
		}
	}
	if old := c.subs[r.SessionID]; old != nil {
		old.Close()
		delete(c.subs, r.SessionID)
	}
	sub, info, err := w.Subscribe()
	if err != nil {
		return nil, err
	}
	c.subs[r.SessionID] = sub
	// 先入队订阅确认，再允许推送协程投递事件，避免确认晚于首批事件。
	c.send(protocol.Reply(r.RequestID, map[string]any{"subscribed": true, "epoch": info.Epoch, "seq": info.Seq, "replay": true}, nil))
	go func() {
		defer sub.Close()
		for {
			m, err := sub.Next(c.ctx)
			if err != nil {
				if c.ctx.Err() == nil {
					c.send(protocol.Message{Version: 1, Kind: "control", SessionID: r.SessionID, Event: "bridge.subscription_closed", Data: map[string]bool{"resyncRequired": true}})
				}
				return
			}
			// 顺手维护扩展状态快照，供页面刷新后立即显示。
			// 只处理 setStatus，其余扩展方法不进状态表。
			if raw, isRaw := m.Data.(json.RawMessage); isRaw {
				if key, text, ok := parseSetStatus(raw); ok {
					c.server.extState.update(key, text)
				}
			}
			if !c.send(m) {
				return
			}
		}
	}()
	return noReply{}, nil
}

// unsubscribe 只解除该连接的订阅，不中断任务。
func (c *connection) unsubscribe(r protocol.Request) (any, error) {
	if err := protocol.Decode(r.Params, &struct{}{}); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if sub := c.subs[r.SessionID]; sub != nil {
		sub.Close()
		delete(c.subs, r.SessionID)
	}
	c.mu.Unlock()
	return map[string]bool{"subscribed": false}, nil
}

// dispatch 执行一条命令；ctx 只约束等待，不把浏览器断开当作取消任务。
func (c *connection) dispatch(ctx context.Context, r protocol.Request) (any, error) {
	switch r.Method {
	case "session.subscribe":
		return c.subscribe(ctx, r)
	case "session.unsubscribe":
		return c.unsubscribe(r)
	default:
		return c.server.dispatchCommon(ctx, r, c)
	}
}

// connContext 实现 connSink。
func (c *connection) connContext() context.Context { return c.ctx }

// noReply 表示该命令已自行发送响应，无需框架再补一条。
// 当前用于订阅：先发确认，再由推送协程持续发送事件。
type noReply struct{}

// serveExport 下载导出的 HTML。文件名只能来自 safeExportName 的产出，
// 这里再拒绝路径分隔符并核对父目录，双保险。
func (s *Server) serveExport(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ui/exports/")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		writeError(w, 400, protocol.E("invalid_params", "文件名不合法"))
		return
	}
	target := filepath.Join(s.exportDir, name)
	if filepath.Dir(target) != filepath.Clean(s.exportDir) {
		writeError(w, 400, protocol.E("invalid_params", "文件名不合法"))
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		writeError(w, 404, protocol.E("not_found", "导出文件不存在"))
		return
	}
	body, err := os.ReadFile(target)
	if err != nil {
		writeError(w, 404, protocol.E("not_found", "导出文件不存在"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Vary", "Accept-Encoding")
	encoding := takeEncoding(w)
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.WriteHeader(200)
	_, _ = presentation.Compress(w, body, encoding)
}
