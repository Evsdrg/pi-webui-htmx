# Pi 兼容矩阵

核对日期：2026-09-27。基线：本机 `@earendil-works/pi-coding-agent` 0.85.1。

本文区分 **上游支持、当前代码存在、验证范围、刻意排除项**。设计决策见 [architecture.md](architecture.md)。

## 源码依据

Pi 包内 `docs/rpc.md`、`docs/session-format.md`、`dist/modes/rpc/rpc-mode.js`、`rpc-types.d.ts` 和 `dist/core/session-manager.js`；桥以 `internal/transport/server.go` 的 `SupportedMethods`、`internal/runtime/` 与 `internal/pi/` 为准；UI 以 `src/modules/` 与 `ui-manifest.json` 为准。Pi Web（另一个实现）仅作行为对照，它的 SDK 能力不等于标准 RPC 能力。运行程序只通过 `--pi` 选可执行文件，不硬编码本机包安装路径。

## 命令与功能现状

| 能力 | Pi 0.85.1 / 所有权 | 当前桥与 UI | 边界 |
|---|---|---|---|
| RPC 启动、创建、恢复 | `--mode rpc`、`--session` | ✅ 已接线 | `get_state` 握手；列表、普通历史不启动 worker |
| prompt / 取消 / 状态 | `prompt`、`clear_queue`、`abort`、`get_state` | ✅ 已接线 | 接受不等于完成；结果未知不自动重发 |
| 增量与完成事件 | `message_update`、`message_end`、`agent_settled` | ✅ 已接线 | 缺口时明确 resync，不声称无损重连 |
| 模型、思考强度 | `get_available_models`、`set_model`、thinking 命令 | ✅ 已接线 | 用户意图与权威回读分离，启动回读不覆盖首条消息的选择 |
| steering / follow-up | 两类队列及其各自投递模式 | ✅ 已接线 | `streamingBehavior: steer` 与 queue `kind: steering` 是两件事 |
| 压缩、自动压缩、自动重试 | 标准 RPC 命令 | ✅ 已接线 | 长任务按方法声明加长等待；Pi 状态没有自动重试的可靠读回字段 |
| bash | RPC `bash`、`abort_bash` | ✅ 已接线 | 不是 PTY；完整输出走 HTTP 数据通道 |
| new / switch / fork / clone | 标准 RPC 身份变更 | ✅ 已接线 | 身份是事务：冲突在副作用之前处理，成功后提交新 epoch |
| 树、原始条目 | RPC `get_tree`、`get_entries` | ✅ 已接线 | 有 worker 用实时树；无 worker 从磁盘投影（浏览不拉起进程） |
| 历史、搜索、思考/图片读取 | Pi v3 JSONL | ✅ 桥只读实现 | 共用带文件身份校验的偏移索引；有界读取与搜索 |
| 导出、命名、统计 | RPC 支持；Pi Web 另有文件导出路径 | ✅ 已接线 | 只读磁盘投影，零 worker；导出目录有条数与字节配额 |
| select / confirm / input / editor | 需要 UI 回执 | ✅ 已接线 | 超时、非法回复与过大请求都会收敛，不留永久 pending |
| notify / setStatus / setWidget / setTitle / set_editor_text | fire-and-forget | ✅ 通用通道，无插件专属代码 | **setStatus 不需要回执**；状态缓存按 (worker, session, epoch, key) 隔离 |
| 自定义 TUI、footer/header/editor component | RPC 下不转发或 no-op | 🚫 不伪装支持 | 如需网页能力，应由上游提供标准通道 |
| 工具动态配置、原地树导航、reload | 未确认为标准 RPC 命令 | ⚠️ 不承诺支持 | TUI / SDK 方法存在不构成桥支持证据 |
| PTY / 文件 / Git | 桥独立职责 | ✅ 已接线 | 不是 Pi RPC 的安全沙箱；只读 Git 拒绝转换过滤器等隐式执行 |
| 模型配置、供应商 discover/test | 桥管理面 | ✅ 已接线 | 按 revision 写入、`***` 为占位符、禁止远程新增 `!command`；catalog 已接 UI |
| relay / 主动隧道 | 桥独立职责 | ✅ 已接通 | relay 只转发不解析；HTTP+WS+图片+导出闭环；分块未实现 |

## 必须保留的语义

1. RPC 只按 LF 分帧，可接受 CRLF；U+2028/U+2029 不切行。外部 requestId、内部 RPC id、dialog id 相互独立。
2. prompt 响应表示接受、排队或处理；后续失败走事件。`message_end.message` 是完整消息，`tool_execution_update.partialResult` 是累计输出，不能当 delta 拼接。
3. `agent_end` 不等于全部收敛；队列、重试、压缩、直接 bash、扩展对话分别跟踪。
4. 普通列表和历史只读 JSONL，不加载 AgentSession，不触发 Pi 格式迁移。Pi 是会话正文唯一写入方；桥内互斥不能约束不合作的外部 CLI。
5. Pi 的 `models` 是数组，模型 `api` 是协议标识，不是 URL；`baseUrl` 才是地址。读、脱敏、恢复、摘要和校验必须使用同一 schema。
6. 当前大树失败链已确认包括桥的 `MaxFrame=8 MiB` 和 `Client.read()` 超限关闭连接；约 9 MiB 的端到端失败**不能据此判定 Pi 自身有约 9 MiB 上限**。
7. 默认不自动批准项目扩展；启用扩展不是执行隔离。`--offline` 只约束启动联网，不代表 prompt 不会访问模型。
8. 真实 Pi 冒烟使用隔离 agent-dir，不加载生产 MC/密钥，不发送付费模型请求。流式和故障测试使用可控假 Pi。

## 验证范围

桥：`gofmt`、`go vet`、`staticcheck` 干净；`go test -race ./...` 覆盖全部 19 个包。UI：Vitest、typecheck、build、契约与对比度检查通过。浏览器验收覆盖发送、分页、文件树、扩展确认、终端、模型编辑与三视口布局。

⚠️ 通过项不代表所有边界都已覆盖。已知的**未验收**范围：真实模型在云端形态下的长时流式、公网 relay 的跨机 RTT 与丢包、小时级长稳、证书轮换、全主题 axe。

⚠️ 非 Linux 目前存在 PTY 编译缺口，不能写成「可构建但显式拒绝」。SIGKILL 场景的进程回收依赖服务管理器（见 [DEVELOPMENT.md](DEVELOPMENT.md) 的部署要点），手工启动只覆盖直接子进程。

## 范围决定

| 状态 | 能力 | 原因或后续 |
|---|---|---|
| 🚫 明确不做 | OAuth 设备码登录、供应商额度查询 | 用户只使用 API 凭据 |
| 🚫 明确不做 | 插件/技能远程安装、更新、搜索及任意 CLI 透传 | 远程执行面；包由本机 CLI 管理，网页只读清单与版本 |
| 🚫 不提供 | 任意会话正文写入、session.import | 会话持久化归 Pi |
| ⚠️ 暂缓 | Web Push | 当前无需推送链路；不是永远禁止 |
| ⚠️ 未立项 | PWA、版本检查/更新、PDF、Minimap、多语言、worktree 等 | 不能把未实现推断为用户明确不要；另行确定产品范围 |
