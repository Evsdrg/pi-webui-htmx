// Package transport 是桥的 HTTP 与 WebSocket 入口：鉴权、限额、路由与命令分发。
//
// 分发只有一份实现：WebSocket 连接与云端隧道虚拟连接都走 dispatchCommon，
// 两者只在订阅与连接生命周期上不同（见 connSink）。这一点的原因是两套实现
// 曾经各写一份，补发与确认的顺序随即漂移。
//
// HTTP 端点按「调用方期待什么」分为两类，不能混：
//   - 片段端点（htmx 会交换的）：一律 200 + 可读 HTML，错误当内容渲染。
//   - fetch/导航端点（/ui/file-text、/ui/file-image、lazy、/ui/exports/*）：
//     必须保留真实状态码，调用方靠 response.ok 或浏览器行为判断。
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
	"pi-bridge-go/internal/magiccontext"
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
	"files.list", "files.index", "files.stat", "files.read", "files.image", "files.roots",
	"git.status", "git.diff",
}

// wsReadLimit 是 WS 单帧读取上限。
// 取图片附件的最大合法体积（8 张 × 12 MiB base64）再加 1 MiB 封套余量，
// 与 pi.MaxImages / pi.MaxImageDataLen 对齐；两侧不一致就会把合法请求
// 当成超限帧，表现为「发不出图片且连接断开」。
const wsReadLimit = pi.MaxImages*pi.MaxImageDataLen + (1 << 20)

// wsTextBudget 是 WS 响应里文本内容的安全预算。
// 连接层单帧上限是 512 KiB，这里留出 JSON 封套与转义余量；
// 超出即截断并标记 truncated，绝不把超限帧交给连接层。
const wsTextBudget = 448 << 10

// Server 是 HTTP 与 WebSocket 入口，只做接入、鉴权与限额。
type Server struct {
	manager   *run.Manager
	store     *sessions.Store
	terminals *terminal.Manager
	files     *workspace.Files
	piConfig  *management.Config
	discovery management.DiscoveryLimits
	exportDir string
	// sessionContexts 缓存「系统提示词 + 工具定义」；它们只能靠导出一次
	// 会话 HTML 取回，按 (sessionId, epoch) 缓存以免每次开面板都重导。
	sessionContexts *sessionContextCache
	ui              *presentation.Renderer
	extState        *extensionState
	receipts        *storage.Receipts
	metrics         *observe.Metrics
	token, host     string
	// publicOrigin 是部署时显式声明的对外来源；零值表示只接受监听地址本身。
	publicOrigin PublicOrigin
	connections  chan struct{}
	operations   chan struct{}
	claims       *claims
	tunnelBridge *TunnelBridge
}

// Options 是 Server 的构造参数。
//
// 用结构体而不是位置参数：旧签名有 13 个位置参数，其中 token 与 host
// 相邻且同为 string，对调后编译通过、运行期表现为「鉴权永远失败」——
// 这类错误只会在部署后才暴露。字段名让编译器挡住这种错位，
// 而 Validate 把取值约束从调用方（cmd 里那份长度检查）收到构造处。
// 同仓的 runtime.Config、terminal.Config、tunnel.Config 都是这个形状。
type Options struct {
	Manager *run.Manager
	Store   *sessions.Store
	// Terminals、Files、Config 都是必需的：它们对应实际功能面，
	// 而 UI 与 Tunnel 可以缺席（未配 --ui-dir、未启用 --relay）。
	Terminals    *terminal.Manager
	Files        *workspace.Files
	Config       *management.Config
	Discovery    management.DiscoveryLimits
	ExportDir    string
	Receipts     *storage.Receipts
	Metrics      *observe.Metrics
	Token        string
	Host         string
	PublicOrigin PublicOrigin
	UI           *presentation.Renderer
	// WorkspaceRoot 仅用于在构造失败时给出可读的提示（哪个目录不可用）。
	WorkspaceRoot string
}

// Validate 检查构造参数。错误信息点名具体字段，方便对应到启动参数。
func (o Options) Validate() error {
	// Token 的长度下限不是形式主义：它是唯一凭据，短 token 可被暴力枚举。
	// 这条检查以前只在 cmd 里做过，测试构造路径完全没有它——
	// 把 token 与 host 写反时两种路径都不会报错。
	if len(o.Token) < 32 {
		return errors.New("Options.Token 至少需要 32 个字符（检查是否与 Host 写反）")
	}
	if o.Host == "" {
		return errors.New("Options.Host 不能为空：它同时用于 Host 校验与 Cookie 签名")
	}
	switch {
	case o.Manager == nil:
		return errors.New("Options.Manager 不能为空")
	case o.Store == nil:
		return errors.New("Options.Store 不能为空")
	case o.Terminals == nil:
		return errors.New("Options.Terminals 不能为空")
	case o.Files == nil:
		return errors.New("Options.Files 不能为空")
	case o.Config == nil:
		return errors.New("Options.Config 不能为空")
	}
	return nil
}

// New 构造入口。publicOrigin 为零值时只接受 Host 本身（本地用法不变）。
func New(opts Options) (*Server, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	server := &Server{
		manager:         opts.Manager,
		store:           opts.Store,
		terminals:       opts.Terminals,
		files:           opts.Files,
		piConfig:        opts.Config,
		discovery:       opts.Discovery,
		exportDir:       opts.ExportDir,
		sessionContexts: newSessionContextCache(),
		ui:              opts.UI,
		extState:        newExtensionState(64),
		receipts:        opts.Receipts,
		metrics:         opts.Metrics,
		token:           opts.Token,
		host:            opts.Host,
		publicOrigin:    opts.PublicOrigin,
		connections:     make(chan struct{}, 8),
		operations:      make(chan struct{}, 16),
		claims:          newClaims(1024),
	}
	// magic-context 的只读视图挂在渲染器上：面板是服务端片段，
	// 由 presentation 负责取数，传输层只管路由与鉴权。
	// ui 为 nil 表示没配 UI 包目录，那时整个 /ui/* 都不该被请求到。
	// 这里必须显式判断：渲染器不再为 nil 接收者提供容错（见 G12）。
	if opts.UI != nil {
		opts.UI.SetMagicContext(magiccontext.NewStore())
	}
	return server, nil
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
// handleUIResponse 处理扩展对话回执。
//
// 表单提交（application/x-www-form-urlencoded）或 JSON 都可以。
// 三种语义互斥，优先级 cancelled > confirmed > value，
// 与 Pi 的 parseResponse 一致。
func (s *Server) handleUIResponse(w http.ResponseWriter, r *http.Request) {
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ui/sessions/"), "/ui-response")
	sessionID := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		sessionID = rest[:i]
	}
	if !sessions.ValidID(sessionID) {
		writeError(w, encoding, 400, protocol.E("invalid_params", "会话 ID 不合法"))
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
			writeError(w, encoding, 400, protocol.E("invalid_params", "请求体不是合法 JSON"))
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeError(w, encoding, 400, protocol.E("invalid_params", "表单解析失败"))
			return
		}
		p.ID = r.FormValue("id")
		p.Value = r.FormValue("value")
		p.Confirmed = r.FormValue("confirmed")
		p.Cancelled = r.FormValue("cancelled")
	}
	if p.ID == "" {
		writeError(w, encoding, 400, protocol.E("invalid_params", "缺少对话 id"))
		return
	}

	wkr, err := s.manager.Get(sessionID)
	if err != nil {
		writeError(w, encoding, 400, protocol.E("worker_not_running", "会话没有活跃的工作进程"))
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
		writeError(w, encoding, 400, err)
		return
	}
	// htmx 默认会把响应换入目标；回执成功无需换任何内容。
	w.WriteHeader(204)
}

// serveUI 处理 UI 层请求，按契约分成两类：
//
//   - 外壳与静态资源（/、/assets/）：保留真实状态码，浏览器与缓存按它判断。
//   - htmx 片段（/ui/*）：一律 200 + 可读 HTML，见 renderFragment。
//
// 两类曾经混在同一个 381 行函数里，而它们的错误约定正好相反——
// 把「渲染失败要回 500」与「状态失败要回 200 + 说明」写在同一个 switch 中，
// 改动时很容易把一条约定套到另一类端点上。因此按契约拆成两个函数。
//
// 流式对话不走这里，走 WS（见 wsHandler）。
//
// 返回 true 表示已处理，调用方应直接返回。
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) bool {
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	// 文件全文是桥能力，不依赖 UI 包：大内容走 HTTP，
	// WS 只留控制帧与截断预览（B07）。
	if r.URL.Path == "/ui/file-text" {
		text, truncated, _, ferr := s.files.Read(r.URL.Query().Get("path"))
		if ferr != nil {
			writeError(w, encoding, 400, ferr)
			return true
		}
		writeText(w, encoding, text, truncated)
		return true
	}
	if s.ui == nil {
		// 未配置 UI 包时 UI 路由整体不存在，回 404 让调用方继续。
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/ui/") {
			writeError(w, encoding, 404, protocol.E("not_found", "未配置 UI 包，使用 --ui-dir 指定 pi-webui-htmx 目录"))
			return true
		}
		return false
	}

	if s.serveUIAssets(w, r, encoding) {
		return true
	}
	return s.serveUIFragments(w, r, encoding)
}

// serveUIAssets 处理外壳与静态资源。
//
// 与片段的关键区别：这些响应带真实状态码与缓存语义，调用方是浏览器本身
// 或缓存层，而不是 htmx 的交换逻辑。资源名含内容哈希，因此可长期不可变
// 缓存；首页随 ?session= 变化，不做缓存。
func (s *Server) serveUIAssets(w http.ResponseWriter, r *http.Request, encoding presentation.Encoding) bool {
	switch path := r.URL.Path; {
	case path == "/":
		id := r.URL.Query().Get("session")
		if id != "" && !sessions.ValidID(id) {
			writeError(w, encoding, 400, protocol.E("invalid_params", "会话 ID 不合法"))
			return true
		}
		html, err := s.ui.RenderShell(id)
		if err != nil {
			writeError(w, encoding, 500, err)
			return true
		}
		writeHTML(w, encoding, html)
		return true

	case strings.HasPrefix(path, "/assets/"):
		name := strings.TrimPrefix(path, "/assets/")
		body, mime, ok := s.ui.Asset(name, encoding)
		if !ok {
			writeError(w, encoding, 404, protocol.E("not_found", "资源不存在"))
			return true
		}
		// 文件名带内容哈希，可长期不可变缓存。
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", mime)
		// Vary 必须无条件声明：共享缓存按 Accept-Encoding 区分变体，
		// 只在压缩分支设置会让 identity 响应缺少 Vary，
		// 代理可能把 brotli 变体回给不支持它的客户端。
		w.Header().Set("Vary", "Accept-Encoding")
		if encoding != "" {
			w.Header().Set("Content-Encoding", encoding.Header())
		}
		w.WriteHeader(200)
		_, _ = w.Write(body)
		return true
	}
	return false
}

// serveUIFragments 处理 htmx 片段。
//
// 约定：**一律 200 + 一段可读 HTML**，包括「worker 未启动」这类前置状态。
// 理由是 htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容上、
// 没有任何解释（真机复现过：未启动 worker 时点「会话信息」，请求发出但
// 界面一直显示占位文字）。
//
// 下面三处例外是有意的：它们不是片段语义，而是给前端的**状态信号**，
// 由 workbench.ts 的 `htmx:beforeSwap` 处理器读取。
//   - /ui/sessions/{id}/history 的 204 + X-Session-Unsaved：分支尚未落盘
//   - /ui/extensions/dialog/{id} 的 204：对话已被回答，移除占位
//   - 明确非法参数（含越界路径）回 400：这类请求不可能来自本仓前端
func (s *Server) serveUIFragments(w http.ResponseWriter, r *http.Request, encoding presentation.Encoding) bool {
	path := r.URL.Path
	switch {
	case path == "/ui/sessions":
		return s.renderFragment(w, encoding, func() (string, error) {
			offset, err := number(r, "offset", 0)
			if err != nil {
				return "", err
			}
			limit, err := number(r, "limit", 50)
			if err != nil {
				return "", err
			}
			list, err := s.store.List(r.Context(), offset, limit)
			if err != nil {
				return "", err
			}
			return s.ui.RenderSessionsPage(list, r.URL.Query().Get("selected"), offset)
		})

	// 惰性内容：思考文本与工具结果图片。
	// 历史页只带占位符，base64 图片和大段思考等用户点了才取——
	// 否则每一页翻迁都要为当时并没看的内容付带宽。
	// 文件查看器的图片：与惰性工具图片同理，二进制不该经过 UTF-8 转换。
	case path == "/ui/file-image":
		s.serveFileImage(w, r)
		return true

	case strings.HasPrefix(path, "/ui/sessions/") && strings.HasSuffix(path, "/lazy"):
		s.serveLazy(w, r, path)
		return true

	case strings.HasPrefix(path, "/ui/sessions/") && strings.HasSuffix(path, "/history"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/ui/sessions/"), "/history")
		if !sessions.ValidID(id) {
			writeError(w, encoding, 400, protocol.E("invalid_params", "会话 ID 不合法"))
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
			var historyError *protocol.Error
			if errors.As(perr, &historyError) && historyError.Code == "not_found" {
				// Pi 会等第一条 assistant 回复才写出新分支。仅在文件确实
				// 不存在且该身份仍有活跃 worker 时返回 204；非法叶子仍报错。
				_, findErr := s.store.Find(r.Context(), id)
				var findError *protocol.Error
				if errors.As(findErr, &findError) && findError.Code == "not_found" {
					if _, workerErr := s.manager.Get(id); workerErr == nil {
						w.Header().Set("X-Session-Unsaved", "1")
						w.Header().Set("Cache-Control", "no-store")
						w.WriteHeader(http.StatusNoContent)
						return true
					}
				}
			}
			// 其余失败（会话不存在、分支游标不在所选分支上）是**状态类**失败，
			// 按片段约定当内容渲染。以前这里 writeError 400——而 htmx 不交换
			// 4xx，于是「会话已被删除」在界面上表现为停留在旧历史上，
			// 没有任何提示，比一句「会话不存在」更难受。
			s.fragmentIssue(w, encoding, perr)
			return true
		}
		html, herr := s.ui.RenderHistory(id, page)
		if herr != nil {
			s.fragmentIssue(w, encoding, herr)
			return true
		}
		writeHTML(w, encoding, html)
		return true

	case path == "/ui/models":
		return s.renderFragment(w, encoding, func() (string, error) {
			out, err := s.piConfig.Models()
			if err != nil {
				return "", err
			}
			return s.ui.RenderModels(presentation.ConfigModels(out), "")
		})

	case path == "/ui/diff":
		return s.renderFragment(w, encoding, func() (string, error) {
			diff, truncated, err := s.files.GitDiff(r.Context(), r.URL.Query().Get("path"), r.URL.Query().Get("staged") == "true", 512<<10)
			if err != nil {
				return "", err
			}
			html, err := s.ui.RenderDiff("", presentation.ParseDiff(diff))
			if err != nil {
				return "", err
			}
			if truncated {
				html += "<p class=\"empty-note\">差异已达到预览上限。</p>"
			}
			return html, nil
		})

	case path == "/ui/packages":
		return s.renderFragment(w, encoding, func() (string, error) {
			pkgs, err := s.piConfig.Packages(r.Context(), s.discovery)
			if err != nil {
				return "", err
			}
			return s.ui.RenderPackages(packageRows(pkgs))
		})

	case path == "/ui/files":
		return s.renderFragment(w, encoding, func() (string, error) {
			root := r.URL.Query().Get("path")
			if root == "" {
				if roots := s.files.Roots(); len(roots) > 0 {
					root = roots[0]
				}
			}
			entries, truncated, err := s.files.List(root)
			if err != nil {
				return "", err
			}
			return s.ui.RenderFiles(root, fileRows(entries), truncated)
		})

	case path == "/ui/dirs":
		s.serveDirs(w, r, encoding)
		return true

	case path == "/ui/mc":
		s.serveMagicContext(w, r, encoding)
		return true

	case path == "/ui/git-status":
		return s.renderFragment(w, encoding, func() (string, error) {
			status, err := s.files.GitStatus(r.Context(), r.URL.Query().Get("path"))
			if err != nil {
				return "", err
			}
			return s.ui.RenderGitStatus(gitStatus(status))
		})

	case path == "/ui/search":
		return s.renderFragment(w, encoding, func() (string, error) {
			query := r.URL.Query().Get("q")
			var hits []presentation.SearchHit
			if query != "" {
				result, err := s.store.Search(r.Context(), query, sessions.DefaultSearchLimits())
				if err != nil {
					return "", err
				}
				hits = searchHits(result.Matches)
			}
			return s.ui.RenderSearch(query, hits)
		})

	case path == "/ui/branch":
		return s.renderFragment(w, encoding, func() (string, error) {
			// 分支树需要活动 worker：get_tree 是 Pi 进程内的命令，
			// 没有纯磁盘等价物。未启动时给可读提示，不静默返回空树。
			worker, err := s.manager.Get(r.URL.Query().Get("sessionId"))
			if err != nil {
				return "", err
			}
			tree, err := worker.Tree(r.Context())
			if err != nil {
				return "", err
			}
			// fork 信息取不到不算失败：分支树本身仍可导航。
			forks, err := worker.ForkMessages(r.Context())
			if err != nil {
				forks = map[string]any{}
			}
			rows, forkRows := presentation.BranchRows(tree, forks, r.URL.Query().Get("leafId"))
			return s.ui.RenderBranch(rows, forkRows)
		})

	case path == "/ui/system":
		return s.renderFragment(w, encoding, func() (string, error) {
			value, err := s.sessionContext(r.Context(), r.URL.Query().Get("sessionId"))
			if err != nil {
				return "", err
			}
			return s.ui.RenderSystem(value.SystemPrompt)
		})

	case path == "/ui/tools":
		return s.renderFragment(w, encoding, func() (string, error) {
			value, err := s.sessionContext(r.Context(), r.URL.Query().Get("sessionId"))
			if err != nil {
				return "", err
			}
			return s.ui.RenderTools(value.Tools)
		})

	case path == "/ui/stats":
		return s.renderFragment(w, encoding, func() (string, error) {
			worker, err := s.manager.Get(r.URL.Query().Get("sessionId"))
			if err != nil {
				return "", err
			}
			meta := s.statsMeta(r.Context(), worker)
			stats, statsErr := worker.Stats(r.Context())
			// 统计失败仍要输出会话事实：失败回合会让 get_session_stats 整体报错，
			// 那时面板至少还应告诉你「这是哪个会话」——所以这里不返回错误，
			// 把 statsErr 交给模板渲染成说明。
			return s.ui.RenderStats(meta, stats, statsErr)
		})

	case path == "/ui/extensions/status":
		html, rerr := s.ui.RenderExtensionStatus(s.extensionStatuses())
		if rerr != nil {
			writeError(w, encoding, 500, rerr)
			return true
		}
		writeHTML(w, encoding, html)
		return true

	case strings.HasPrefix(path, "/ui/extensions/dialog/"):
		// GET：渲染某个待回复对话。对话不存在时返回 204，
		// 让 htmx 移除占位而不是显示错误。
		id := strings.TrimPrefix(path, "/ui/extensions/dialog/")
		if id == "" || strings.ContainsAny(id, "/\\") {
			writeError(w, encoding, 400, protocol.E("invalid_params", "对话 ID 不合法"))
			return true
		}
		raw, found := s.pendingDialog(id)
		if !found {
			w.WriteHeader(204)
			return true
		}
		d, derr := presentation.DialogFromPi(id, r.URL.Query().Get("sessionId"), raw)
		if derr != nil {
			writeError(w, encoding, 400, derr)
			return true
		}
		html, rerr := s.ui.RenderExtensionDialog(d)
		if rerr != nil {
			writeError(w, encoding, 500, rerr)
			return true
		}
		writeHTML(w, encoding, html)
		return true

	case path == "/ui/extensions/dialogs":
		// 当前会话全部待回复对话，供页面加载时恢复。
		sessionID := r.URL.Query().Get("sessionId")
		items, ferr := s.pendingDialogsFor(sessionID)
		if ferr != nil {
			writeError(w, encoding, 400, ferr)
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
			writeError(w, encoding, 500, rerr)
			return true
		}
		writeHTML(w, encoding, html)
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
	// 编码协商是纯函数，写响应的辅助函数各自按需调用即可；
	// 不再需要一次协商后靠内部 Header 键往下传。
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	if !s.hostAllowed(r.Host) {
		writeError(w, encoding, http.StatusForbidden, protocol.E("host_denied", "Host 不在预期范围内"))
		return
	}
	if !s.originAllowed(r) {
		writeError(w, encoding, http.StatusForbidden, protocol.E("origin_denied", "未启用跨源访问"))
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, encoding, 200, map[string]any{"ok": true})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth" {
		if !s.bearer(r) {
			writeError(w, encoding, 401, protocol.E("unauthorized", "需要 Bearer token"))
			return
		}
		expires := time.Now().Add(8 * time.Hour)
		exp := strconv.FormatInt(expires.Unix(), 10)
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: exp + "." + s.signature(exp), HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode, Path: "/", Expires: expires, MaxAge: 8 * 60 * 60})
		writeJSON(w, encoding, 200, map[string]any{"ok": true})
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
		writeError(w, encoding, 401, protocol.E("unauthorized", "需要身份验证"))
		return
	}
	// 扩展对话回执是 POST，且要转成 WS 命令 session.ui_response。
	// 必须在通用的「只接受 GET」之前处理。
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/ui/sessions/") && strings.HasSuffix(r.URL.Path, "/ui-response") {
		if s.ui == nil {
			writeError(w, encoding, 404, protocol.E("not_found", "未配置 UI 包"))
			return
		}
		s.handleUIResponse(w, r)
		return
	}

	if r.Method != http.MethodGet {
		writeError(w, encoding, 405, protocol.E("invalid_request", "请求方法不被允许"))
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
		writeJSON(w, encoding, 200, map[string]any{
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
			writeError(w, encoding, 400, err)
			return
		}
		offset, err := number(r, "offset", 0)
		if err != nil {
			writeError(w, encoding, 400, err)
			return
		}
		list, err := s.store.List(r.Context(), offset, limit)
		respond(w, encoding, list, err)
	case "/api/v1/metrics":
		sessionStats := s.store.Index().Stats()
		if n, ok := sessionStats["sessions"].(int); ok {
			s.metrics.SetSessionsIndexed(n)
		}
		out := map[string]any{"metrics": s.metrics.Snapshot(), "sessions": sessionStats, "receipts": s.receipts.Stats(), "workers": s.manager.List(), "terminals": s.terminals.List()}
		if s.tunnelBridge != nil {
			out["tunnel"] = s.tunnelBridge.Stats()
		}
		writeJSON(w, encoding, 200, out)
	case "/api/v1/ws":
		s.serveWS(w, r)
	default:
		prefix := "/api/v1/sessions/"
		suffix := "/history"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
			limit, err := number(r, "limit", 50)
			if err != nil {
				writeError(w, encoding, 400, err)
				return
			}
			s.metrics.HistoryRequest()
			page, err := s.store.History(r.Context(), id, r.URL.Query().Get("leafId"), r.URL.Query().Get("before"), limit)
			respond(w, encoding, page, err)
			return
		}
		writeError(w, encoding, 404, protocol.E("not_found", "接口不存在"))
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
// 下面几个转换把上游类型映射成渲染行。
//
// 以前这里用一次 JSON 往返（toAnyMaps）把结构体转成 map，渲染层再按键取值
// 拼回类型化行——为了回到类型先绕一圈 JSON（500 项目录实测约 590µs/360KB）。
// 改成逐字段赋值后，键名由编译器校验，也不再产生中间表示。
func fileRows(entries []workspace.Entry) []presentation.FileRow {
	rows := make([]presentation.FileRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, presentation.FileRow{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size})
	}
	return rows
}

func packageRows(pkgs []management.PackageInfo) []presentation.PackageRow {
	rows := make([]presentation.PackageRow, 0, len(pkgs))
	for _, p := range pkgs {
		rows = append(rows, presentation.PackageRow{
			Name:      p.Name,
			Source:    p.Source,
			Version:   p.Version,
			Latest:    p.Latest,
			HasUpdate: p.HasUpdate,
			Disabled:  p.Disabled,
			Error:     p.Error,
		})
	}
	return rows
}

func searchHits(matches []sessions.Match) []presentation.SearchHit {
	rows := make([]presentation.SearchHit, 0, len(matches))
	for _, m := range matches {
		rows = append(rows, presentation.SearchHit{
			SessionID: m.SessionID,
			EntryID:   m.EntryID,
			Title:     m.Title,
			Cwd:       m.Cwd,
			Snippet:   m.Snippet,
		})
	}
	return rows
}

func gitStatus(status workspace.GitStatus) presentation.GitStatus {
	rows := make([]presentation.GitFileRow, 0, len(status.Files))
	for _, f := range status.Files {
		rows = append(rows, presentation.GitFileRow{Status: f.Status, Path: f.Path})
	}
	return presentation.GitStatus{Branch: status.Branch, Clean: status.Clean, Truncated: status.Truncated, Files: rows}
}

// writeHTML 输出 HTML 片段。htmx 靠 Content-Type 决定如何处理响应。
// writeHTML 写一段 HTML 片段。encoding 由调用方现场协商后传入。
// 曾经用内部 Header 键在 ServeHTTP 与写函数之间偷递，还要靠「读取后即删」
// 才不外泄——数据流隐式化，纯属为了少改调用点签名。
func writeHTML(w http.ResponseWriter, encoding presentation.Encoding, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")
	body := []byte(html)
	// 必须与 ShouldCompress 一致：Compress 在 body 小于阈值时直接写原文，
	// 这里若仍然标 Content-Encoding，客户端会按该编码解压明文并失败。
	// 浏览器表现为 fetch 直接 reject（"Failed to fetch"），任何小于 1 KB
	// 的 HTML 片段——历史分页、扩展对话框、包清单——全都换不进去。
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding.Header())
	}
	w.WriteHeader(200)
	_, _ = presentation.Compress(w, body, encoding)
}

// writeText 输出纯文本文件内容。截断标记放在响应头里，
// 让前端能区分「文件就这么长」和「桥做了预算截断」。
func writeText(w http.ResponseWriter, encoding presentation.Encoding, text string, truncated bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")
	if truncated {
		w.Header().Set("X-Truncated", "1")
	}
	body := []byte(text)
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding.Header())
	}
	w.WriteHeader(200)
	_, _ = presentation.Compress(w, body, encoding)
}

func writeJSON(w http.ResponseWriter, encoding presentation.Encoding, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		body = []byte(`{"error":"encode_failed"}`)
		status = 500
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Vary", "Accept-Encoding")
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding.Header())
	}
	w.WriteHeader(status)
	_, _ = presentation.Compress(w, body, encoding)
}

// writeError 把内部错误转成协议错误响应。
func writeError(w http.ResponseWriter, encoding presentation.Encoding, status int, err error) {
	writeJSON(w, encoding, status, protocol.Reply("", nil, err))
}

// respond 按错误码映射 HTTP 状态；未识别的错误一律按 500 处理。
func respond(w http.ResponseWriter, encoding presentation.Encoding, data any, err error) {
	if err == nil {
		writeJSON(w, encoding, 200, data)
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
	writeError(w, encoding, status, err)
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

// sendRaw 按补发的整批截止时间有界排队；单帧大小与队列预算不能被突破。
func (c *connection) sendRaw(ctx context.Context, b []byte) bool {
	return enqueueBounded(ctx, c.out, &c.queued, b)
}

func (c *connection) send(m protocol.Message) bool {
	b, err := json.Marshal(m)
	if err != nil || len(b) > outboundFrameLimit {
		c.cancel()
		return false
	}
	ctx, cancel := context.WithTimeout(c.ctx, outboundWait)
	defer cancel()
	if !enqueueBounded(ctx, c.out, &c.queued, b) {
		c.cancel()
		return false
	}
	return true
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
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	select {
	case s.connections <- struct{}{}:
		defer func() { <-s.connections }()
	default:
		writeError(w, encoding, 429, protocol.E("limit_exceeded", "连接数量已达上限"))
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.wsOriginPatterns()})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	// 读上限必须覆盖命令本身的合法体积，而不是随手给个 1 MiB：
	// 图片附件按 pi.MaxImages × pi.MaxImageDataLen 计，约 96 MiB。
	// 旧值 1 MiB 让稍大的图片不仅发不出去，还会因超限直接断开连接（U05）。
	ws.SetReadLimit(wsReadLimit)
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
		ok, urgent := s.admit(req, func(m protocol.Message) { c.send(m) })
		if !ok {
			continue
		}
		sem := c.normal
		if urgent {
			sem = c.urgent
		}
		select {
		case sem <- struct{}{}:
		default:
			s.claims.finish(req.RequestID)
			c.send(protocol.Reply(req.RequestID, nil, protocol.E("busy", "该连接的在途命令数已达上限")))
			continue
		}
		if !urgent {
			select {
			case s.operations <- struct{}{}:
			default:
				<-sem
				s.claims.finish(req.RequestID)
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
			s.runCommand(c, req)
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

// recordReceipt 落一条命令回执，供跨重启去重与对账。
func (s *Server) recordReceipt(req protocol.Request, err error) {
	if !protocol.RecordsOutcome(req.Method) {
		return
	}
	s.storeReceipt(storage.Receipt{
		RequestID:   req.RequestID,
		SessionID:   req.SessionID,
		Method:      req.Method,
		Outcome:     outcomeFor(err),
		Fingerprint: requestFingerprint(req),
	})
}

// storeReceipt 落一条回执并统计失败次数。
//
// 写失败不改变命令结果——命令已经执行或已经失败，回执只是记录——
// 但必须可观测：回执是「命令是否执行过」的唯一依据，静默失败会让
// 重启后的对账失去基础，而 /healthz 的 receipts.degraded 正是为此存在。
//
// 「没有启用回执存储」（ErrNotEnabled）是配置事实，不算故障，不计数。
func (s *Server) storeReceipt(rec storage.Receipt) {
	if s.receipts == nil {
		return
	}
	if err := s.receipts.Record(rec); err != nil && !errors.Is(err, storage.ErrNotEnabled) {
		if s.metrics != nil {
			s.metrics.ReceiptFailed()
		}
	}
}

// connSink 是连接相关的少量能力：生命周期上下文、发送帧、登记终端订阅、
// 以及该连接自己的命令分发。WebSocket 连接与隧道虚拟连接各自实现它，
// 从而共用同一份执行与准入逻辑，同时保留各自的订阅实现。
type connSink interface {
	connContext() context.Context
	send(m protocol.Message) bool
	sendRaw(ctx context.Context, b []byte) bool
	trackTerminal(id string, sub *terminal.Subscription)
	dropTerminal(id string)
	trackSubscription(id string, sub *run.Subscription)
	existingSubscription(id string) *run.Subscription
	dispatch(ctx context.Context, r protocol.Request) (any, error)
}

// dispatchCommon 执行除订阅以外的命令。
// WebSocket 连接与隧道虚拟连接共用这一份实现，避免两处逻辑漂移。
// dispatchCommon 执行除订阅以外的命令。
//
// WebSocket 连接与隧道虚拟连接共用这一份实现，避免两处逻辑漂移。
// 它只做三件事：与 Pi 生命周期无关的两条命令、按 session.* 前缀取工作进程、
// 按域分发。具体命令的参数解码与业务调用在 dispatch.go 里分域摆放——
// 以前这里是一个 696 行、60 个 case 的函数，任何一条命令的改动都要在
// 近七百行里定位。
//
// 顺序有意如此：worker.list 与 session.start 必须在取工作进程之前处理，
// 因为 start 的任务正是创建那个进程。
func (s *Server) dispatchCommon(ctx context.Context, r protocol.Request, sink connSink) (any, error) {
	switch r.Method {
	case "worker.list":
		if err := decodeEmpty(r.Params); err != nil {
			return nil, err
		}
		return s.manager.List(), nil
	case "session.start":
		var p struct {
			Cwd string `json:"cwd"`
			// ToolPreset 只接受四个受支持的预设名；未知值必须被拒，
			// 而不是悄悄回落成默认工具集。
			ToolPreset string `json:"toolPreset"`
		}
		if err := protocol.Decode(r.Params, &p); err != nil {
			return nil, err
		}
		if !run.ValidToolPreset(p.ToolPreset) {
			return nil, protocol.E("invalid_params", "未知的工具预设")
		}
		w, err := s.manager.StartWithPreset(ctx, r.SessionID, p.Cwd, p.ToolPreset)
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
	// 按域分发。各域的 switch 只认自己的方法名，都不认才算未实现。
	// 顺序不影响正确性（方法名互不重叠），按「谁会用到 w」排列以便阅读。
	//
	// 未处理用哨兵错误表达而不是第三个返回值：域函数内那几十处 return
	// 因此一字未改，这批拆分是纯搬移，行为不可能被搬错。哨兵只在域函数
	// 末尾返回，业务错误不会与它混淆。
	for _, dispatch := range []func() (any, error){
		func() (any, error) { return s.dispatchRun(ctx, r, w) },
		func() (any, error) { return s.dispatchSession(ctx, r, w) },
		func() (any, error) { return s.dispatchBash(ctx, r, w) },
		func() (any, error) { return s.dispatchDialogs(ctx, r, w) },
		func() (any, error) { return s.dispatchSessionOps(ctx, r) },
		func() (any, error) { return s.dispatchConfig(ctx, r) },
		func() (any, error) { return s.dispatchWorkspace(ctx, r) },
		func() (any, error) { return s.dispatchTerminal(r, sink) },
	} {
		data, err := dispatch()
		if !isUnhandled(err) {
			return data, err
		}
	}
	return nil, protocol.E("unsupported_method", "A 阶段未实现该方法")
}

// subscribe 处理事件订阅：先做游标补发，再注册有界队列并持续推送。
// subscribeWithReplay 是订阅命令的共用实现，WebSocket 连接与隧道虚拟连接
// 都用它，避免两处各写一份补发/确认顺序而后漂移。
//
// 关键点：补发快照与订阅注册必须在 worker 内一次持锁完成，否则两次锁之间
// 发布的事件既不在快照里也不会进入实时订阅，重连后静默丢失（B05）。
func (s *Server) subscribeWithReplay(c connSink, r protocol.Request) (any, error) {
	var p struct {
		Epoch    string  `json:"epoch"`
		AfterSeq *uint64 `json:"afterSeq"`
	}
	if err := protocol.Decode(r.Params, &p); err != nil {
		return nil, err
	}
	w, err := s.manager.Get(r.SessionID)
	if err != nil {
		return nil, err
	}
	if c.connContext().Err() != nil {
		return nil, protocol.E("conflict", "连接已关闭")
	}
	cursor := p.Epoch != "" || p.AfterSeq != nil
	after := uint64(0)
	if p.AfterSeq != nil {
		after = *p.AfterSeq
	}
	sub, info, items, ok, err := w.SubscribeWithReplay(p.Epoch, after, cursor)
	if err != nil {
		return nil, err
	}
	if !ok {
		// 补发不了就明确要求重新同步，不伪造无损恢复。
		s.metrics.ReplayMiss()
		return nil, protocol.E("resync_required", "事件游标已失效，请重新读取持久历史后再订阅")
	}
	if len(items) > 0 {
		s.metrics.ReplayHit()
	}
	// 换订阅前先关掉旧订阅，旧 goroutine 必须退出，否则 worker 配额被占用。
	if prev := c.existingSubscription(r.SessionID); prev != nil {
		prev.Close()
	}
	c.trackSubscription(r.SessionID, sub)
	// 补发使用整批截止时间，不能因每帧重新计时拖住操作配额。
	replayCtx, cancel := context.WithTimeout(c.connContext(), outboundWait)
	defer cancel()
	for _, item := range items {
		if !c.sendRaw(replayCtx, item.Payload) {
			sub.Close()
			if c.connContext().Err() != nil {
				return nil, protocol.E("conflict", "连接已关闭")
			}
			s.metrics.ReplayMiss()
			return nil, protocol.E("resync_required", "事件补发超时，请重新读取持久历史后再订阅")
		}
	}
	c.send(protocol.Reply(r.RequestID, map[string]any{"subscribed": true, "epoch": info.Epoch, "seq": info.Seq, "replay": true}, nil))
	go func() {
		defer sub.Close()
		for {
			m, err := sub.Next(c.connContext())
			if err != nil {
				if c.connContext().Err() == nil {
					c.send(protocol.Message{Version: 1, Kind: "control", SessionID: r.SessionID, Event: "bridge.subscription_closed", Data: map[string]bool{"resyncRequired": true}})
				}
				return
			}
			// 顺手维护扩展状态快照，供页面刷新后立即显示。
			// 只处理 setStatus，其余扩展方法不进状态表。
			if key, text, ok := parseSetStatus(eventPayload(m)); ok && key != "" {
				s.extState.update(key, text)
			}
			if !c.send(m) {
				return
			}
		}
	}()
	return noReply{}, nil
}

func (c *connection) subscribe(ctx context.Context, r protocol.Request) (any, error) {
	return c.server.subscribeWithReplay(c, r)
}

// trackSubscription 实现 connSink。
func (c *connection) trackSubscription(id string, sub *run.Subscription) {
	c.mu.Lock()
	c.subs[id] = sub
	c.mu.Unlock()
}

// existingSubscription 实现 connSink：取回旧订阅供调用方关闭。
func (c *connection) existingSubscription(id string) *run.Subscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[id]
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
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	name := strings.TrimPrefix(r.URL.Path, "/ui/exports/")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		writeError(w, encoding, 400, protocol.E("invalid_params", "文件名不合法"))
		return
	}
	target := filepath.Join(s.exportDir, name)
	if filepath.Dir(target) != filepath.Clean(s.exportDir) {
		writeError(w, encoding, 400, protocol.E("invalid_params", "文件名不合法"))
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		writeError(w, encoding, 404, protocol.E("not_found", "导出文件不存在"))
		return
	}
	body, err := os.ReadFile(target)
	if err != nil {
		writeError(w, encoding, 404, protocol.E("not_found", "导出文件不存在"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// inline 与 Pi Web 的 /export?inline=1 一致：在新标签页里直接阅读，
	// 默认仍是附件下载。两种模式共用同一套鉴权与体积上限。
	if r.URL.Query().Get("inline") == "1" {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	w.Header().Set("Vary", "Accept-Encoding")
	if presentation.ShouldCompress(body, encoding) {
		w.Header().Set("Content-Encoding", encoding.Header())
	}
	w.WriteHeader(200)
	_, _ = presentation.Compress(w, body, encoding)
}

// serveLazy 处理 /ui/sessions/{id}/lazy?entryId=..&kind=thinking|tool-image|user-image&blockIndex=N。
//
// kind 决定取文本还是图片字节。两者都从磁盘上的 JSONL 现读，
// 桥不做任何缓存：内容可能被后续 fork/compact 改变，缓存只会提供陈旧数据。
func (s *Server) serveLazy(w http.ResponseWriter, r *http.Request, path string) {
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/ui/sessions/"), "/lazy")
	if !sessions.ValidID(id) {
		writeError(w, encoding, 400, protocol.E("invalid_params", "会话 ID 不合法"))
		return
	}
	entryID := r.URL.Query().Get("entryId")
	if !sessions.ValidID(entryID) {
		writeError(w, encoding, 400, protocol.E("invalid_params", "条目 ID 不合法"))
		return
	}
	raw := r.URL.Query().Get("blockIndex")
	blockIndex, err := strconv.Atoi(raw)
	if raw == "" || err != nil || blockIndex < 0 {
		writeError(w, encoding, 400, protocol.E("invalid_params", "blockIndex 必须是非负整数"))
		return
	}
	switch r.URL.Query().Get("kind") {
	case "thinking":
		text, err := s.store.Thinking(r.Context(), id, entryID, blockIndex)
		if err != nil {
			writeError(w, encoding, 400, err)
			return
		}
		writeJSON(w, encoding, 200, map[string]string{"thinking": text})
	case "tool-image", "user-image":
		var body []byte
		var mime string
		if r.URL.Query().Get("kind") == "user-image" {
			body, mime, err = s.store.UserImage(r.Context(), id, entryID, blockIndex)
		} else {
			body, mime, err = s.store.ToolImage(r.Context(), id, entryID, blockIndex)
		}
		if err != nil {
			writeError(w, encoding, 400, err)
			return
		}
		// 图片带内容哈希可长期缓存；文件名与 entryId 相关但不含哈希，
		// 所以只用 no-store，避免会话内容变化后浏览器还给旧图。
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	default:
		writeError(w, encoding, 400, protocol.E("invalid_params", "kind 必须是 thinking、tool-image 或 user-image"))
	}
}

// serveFileImage 处理 /ui/file-image?path=...。
// 与 files.read 分开：图片按字节返回，不做 UTF-8 转换——
// 那会破坏像素数据，此前 PNG 就是这样被转成一屏乱码的。
func (s *Server) serveFileImage(w http.ResponseWriter, r *http.Request) {
	encoding := presentation.PickEncoding(r.Header.Get("Accept-Encoding"))
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, encoding, 400, protocol.E("invalid_params", "path 不能为空"))
		return
	}
	body, mime, err := s.files.Image(path)
	if err != nil {
		writeError(w, encoding, 400, err)
		return
	}
	w.Header().Set("Content-Type", mime)
	// 工作区文件可能被外部修改，不能长期缓存。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

// serveDirs 渲染「新建会话」目录选择器的子目录列表。
//
// 与 /ui/files 的区别：只列目录，且要给出「上一级」——但上一级不能越出
// 已配置的工作区根。桥只允许在根内浏览，因此父目录等于根时就不再提供回退，
// 由前端把按钮禁用掉。
func (s *Server) serveDirs(w http.ResponseWriter, r *http.Request, encoding presentation.Encoding) {
	path := r.URL.Query().Get("path")
	if path == "" {
		roots := s.files.Roots()
		if len(roots) == 0 {
			s.fragmentIssue(w, encoding, protocol.E("invalid_params", "尚未配置工作区根"))
			return
		}
		path = roots[0]
	}
	entries, truncated, err := s.files.List(path)
	if err != nil {
		s.fragmentIssue(w, encoding, err)
		return
	}
	dirs := make([]presentation.DirRow, 0, 16)
	for _, e := range entries {
		if e.IsDir {
			dirs = append(dirs, presentation.DirRow{Name: e.Name, Path: e.Path})
		}
	}
	// 父目录：仍在某个根内才提供。用 Roots 逐个判定，避免把 filepath.Dir
	// 的结果直接当成可浏览路径（那会越出沙箱）。
	parent := ""
	for _, root := range s.files.Roots() {
		if rel, rerr := filepath.Rel(root, path); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			parent = filepath.Dir(path)
			break
		}
	}
	html, rerr := s.ui.RenderDirs(path, parent, dirs, truncated)
	if rerr != nil {
		s.fragmentIssue(w, encoding, rerr)
		return
	}
	writeHTML(w, encoding, html)
}

// serveMagicContext 渲染 magic-context 只读面板。
//
// 走片段端点约定：问题一律 200 + 可读 HTML，因为调用方是 htmx，
// 它默认不交换 4xx/5xx，用户会看到一个永远停在占位符的面板。
func (s *Server) serveMagicContext(w http.ResponseWriter, r *http.Request, encoding presentation.Encoding) {
	query := r.URL.Query()
	kind := magiccontext.Kind(query.Get("kind"))
	// 分区不在白名单里时回落默认分区，而不是报错：面板第一次打开
	// 可能带上陈旧或拼错的 kind。
	known := false
	for _, entry := range magiccontext.Kinds {
		if entry.Key == kind {
			known = true
			break
		}
	}
	if !known {
		kind = magiccontext.KindMemories
	}
	offset, _ := number(r, "offset", 0)
	limit, _ := number(r, "limit", 50)
	if limit <= 0 || limit > magiccontext.MaxPageRows {
		limit = 50
	}
	html, err := s.ui.RenderMC(r.Context(), kind, offset, limit, query.Get("category"), query.Get("project"))
	if err != nil {
		s.fragmentIssue(w, encoding, err)
		return
	}
	writeHTML(w, encoding, html)
}
