# 技术栈

本文件列出项目实际使用的技术与版本，供发布说明与依赖审查使用。数字均为 2026-10-01 实测。

## 一句话概括

自托管的 Pi 工作台：**Go 桥**通过 `pi --mode rpc` 子进程驱动 Pi，用 **HTMX + Go 模板**
把界面直接渲染成 HTML 片段推给浏览器。没有前端框架、没有前端路由、没有前端状态库。

## 运行要求

| 组件 | 版本 | 说明 |
|---|---|---|
| Pi | 0.85.1（协议版本 1） | 必须可执行 `pi --mode rpc`；由 `--pi` 指定路径 |
| Go | 1.27（`go.mod` 要求 1.27.1） | 仅构建桥时需要 |
| Node.js | ≥ 20.19（CI 用 26） | 仅构建前端时需要 |
| pnpm | 11.22.0（`packageManager` 锁定） | 前端包管理器 |

## 桥（`pi-bridge-go`）

| 项 | 选型 |
|---|---|
| 语言 | Go，标准库优先 |
| 外部依赖 | **仅 3 个**：`andybalholm/brotli`（响应压缩）、`coder/websocket`（WS，MIT/ISC）、`creack/pty`（终端，MIT） |
| 日志 | `log/slog`（标准库） |
| 模板 | `html/template`（标准库，服务端渲染 + 上下文转义） |
| 协议实现 | 手写 JSON / WebSocket 帧，无 RPC 框架 |
| 规模 | 85 个非测试文件 / 20 375 行；127 个测试文件 / 18 334 行 |
| 包结构 | `transport`（HTTP/WS 路由与分发）、`runtime`（worker 生命周期）、`sessions`（JSONL 读取与投影）、`pi`（RPC 子进程）、`presentation`（模板与静态资源）、`magiccontext`（只读 SQLite）、`workspace` / `git` / `terminal` / `relay` / `tunnel` / `storage` / `observe` 等 |

设计取向：**桥不嵌入 Pi SDK，也不另存一份会话正文**——会话数据始终以 Pi 的 JSONL 为准，
桥只做读取与投影。

## 前端（`pi-webui-htmx`）

### 构建链

| 项 | 版本 | 用途 |
|---|---|---|
| TypeScript | 7.0.2 | 类型检查（**不产 JS 框架代码**） |
| Vite | 8.3.1 | 打包 |
| Tailwind CSS | 4.3.3 | **只用于 preflight 复位**（见下） |
| Vitest | 5.0.2 + jsdom | 单元测试 |

### 浏览器侧运行时依赖（会进产物）

| 依赖 | 用途 |
|---|---|
| `htmx.org` 2.0 | 交互与片段交换（唯一的「框架」） |
| `marked` | Markdown 渲染 |
| `highlight.js` | 代码高亮（配色走 `--code-*` 令牌，支持 5 套主题） |
| `katex` | 数学公式 |
| `dompurify` | 富内容净化 |
| `mermaid` | 图表（**按需动态加载**；传递依赖 `elkjs` 是 EPL-2.0，见 [licensing.md](licensing.md)） |
| `@xterm/xterm` + `addon-fit` | 终端面板 |
| `ansi_up` | ANSI 转义渲染 |

### 样式方案（反直觉的一点）

引入了 Tailwind，但**只用它的 preflight 复位**——模板里没有任何工具类，
所有样式是手写 CSS。样式分三张表，职责严格分离：

| 文件 | 职责 |
|---|---|
| `src/styles/tokens.css` | 5 套主题的调色板与派生令牌（主题色值唯一来源） |
| `src/styles/app.css` | 组件样式，分节；不写任何主题色值 |
| `src/styles/code.css` | highlight.js 配色，按需加载，不进首屏 |

主题色值只允许出现在 `tokens.css`，由 `scripts/check-contract.mjs` 强制；
对比度由 `scripts/check-contrast.mjs` 按 WCAG 4.5:1 逐对校验（5 主题 × 6 文字色 × 2 底色）。

### 结构

| 项 | 数量 |
|---|---|
| Go 模板（`src/templates/`） | 21 个片段 |
| TypeScript 模块 | 23 个 / 3 667 行（只做交互增强：剪贴板、拖动、终端、图表、草稿） |
| 首屏预算 | 自有脚本 + 样式 **30 KiB**（gzip），含 htmx 供应商成本共 **50 KiB** |

## 通信与协议

| 通道 | 用途 |
|---|---|
| `GET /`、`GET /ui/*` | htmx 片段（HTML）；外壳与静态资源用真实状态码，片段端点一律 200 + 可读 HTML |
| `GET /api/v1/*` | JSON API（会话列表、能力声明、导出等） |
| `WS /api/v1/ws` | 命令与事件流：请求带 `requestId`，事件带 `epoch`/`seq`，支持断线补发与 `resync_required` |

鉴权：Bearer token（`PI_BRIDGE_TOKEN`）换取 HttpOnly 会话 Cookie；Host/Origin 严格核对，
非环回监听必须显式声明 `--public-origin`。

## 数据与存储

| 数据 | 位置 | 桥的行为 |
|---|---|---|
| 会话正文 / 工具结果 / 分支 | Pi 的 JSONL（`~/.pi/agent/sessions/`） | 只读；不复制 |
| 桥的配对设备、允许根、设置 | `--state-dir` | 可重建的本地状态 |
| Magic Context 记忆 | `~/.local/share/cortexkit/magic-context/context.db` | **只读**（`mode=ro`，用于「记忆」面板） |

## 部署形态

| 形态 | 组件 |
|---|---|
| 本机 | 桥绑环回，浏览器直连 |
| 公网（当前生产） | 浏览器 → 服务器网关（Caddy：私有证书 + Basic 认证）→ EasyTier 隧道 → 本机桥 |
| 云端中转 | 浏览器 → relay（纯转发，不解析载荷）→ 出站 WSS 隧道 → 本机桥 |

## 测试与质量门

| 门 | 内容 |
|---|---|
| 桥 | `gofmt` / `go vet` / `staticcheck` / **19 个包 `-race` 全绿**（127 个测试文件） |
| 前端 | 22 个测试文件 / 184 项测试、`tsc --noEmit`、`vite build`、UI 包契约校验、主题对比度校验 |
| 契约脚本 | 模板相对路径、主题色值归属、`.hljs` 透明背景、首屏预算、无障碍（axe） |
| CI | 两仓各一个 GitHub Actions workflow，跑上面全部检查 |
