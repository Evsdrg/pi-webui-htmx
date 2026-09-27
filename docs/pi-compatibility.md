# Pi 兼容矩阵

核对日期：2026-09-27。基线：本机 `@earendil-works/pi-coding-agent` 0.85.1。

本文区分 **上游支持、当前代码存在、验证范围、待实现方案**。原 A–E 阶段表示建设顺序，不再用作完成或可靠性保证。审查问题见 [code-audit.md](code-audit.md)，修复设计见 [architecture.md](architecture.md)。P0 测试基础建设已完成；以下产品问题仍待修复。

## 源码依据

Pi 包内 `docs/rpc.md`、`docs/session-format.md`、`dist/modes/rpc/rpc-mode.js`、`rpc-types.d.ts` 和 `dist/core/session-manager.js`；桥以 `internal/transport/server.go` 的 `SupportedMethods`、`internal/runtime/` 与 `internal/pi/` 为准；UI 以 `src/modules/` 与 `ui-manifest.json` 为准。Pi Web 对照目录为相邻的 `../pi-web`，它的 SDK 能力不等于标准 RPC 能力。运行程序只通过 `--pi` 选可执行文件，不硬编码本机包安装路径。

## 命令与功能现状

| 能力 | Pi 0.85.1 / 所有权 | 当前桥与 UI | 边界及审查项 |
|---|---|---|---|
| RPC 启动、创建、恢复 | `--mode rpc`、`--session` | ✅ 已接线 | `get_state` 握手；列表、普通历史不启动 worker |
| prompt / 取消 / 状态 | `prompt`、`clear_queue`、`abort`、`get_state` | ✅ 已接线，⚠️ 边界待修 | 接受不等于完成；B04/B30/B31、U03/U13 |
| 增量与完成事件 | `message_update`、`message_end`、`agent_settled` | ✅ 已接线，⚠️ 恢复待修 | B05/B65、U15/U16；不能声称无损重连 |
| 模型、思考强度 | `get_available_models`、`set_model`、thinking 命令 | ✅ 已接线 | 首次选择可能被回读覆盖，U02 |
| steering / follow-up | 两类队列及其各自投递模式 | ⚠️ 参数与 UI 建模有误 | `streamingBehavior: steer` 与 queue `kind: steering` 不同；B03/U11 |
| 压缩、自动压缩、自动重试 | 标准 RPC 命令 | ✅ 已接线，⚠️ 超时/读回限制 | B66/U14；Pi 状态没有自动重试的可靠读回字段 |
| bash | RPC `bash`、`abort_bash` | ✅ 桥命令存在 | 不是 PTY；完整输出与 WS 大小错配 |
| new / switch / fork / clone | 标准 RPC 身份变更 | ✅ 已接线，⚠️ 生命周期待修 | B09/B10/B64/B65；冲突必须在副作用之前处理 |
| 树、原始条目 | RPC `get_tree`、`get_entries` | ⚠️ 当前仍依赖 worker | B06/U04；拟增加磁盘分页投影，不能把 append 游标当分支分页 |
| 历史、搜索、思考/图片读取 | Pi v3 JSONL | ✅ 桥只读实现 | B11–B13、B28/B29/B37/B38/B43/B52/B72 |
| 导出、命名、统计 | RPC 支持；Pi Web 另有文件导出路径 | ✅ 桥存在，⚠️ 导出需 worker | B45/B76/B77；独立只读导出是待实现方案 |
| select / confirm / input / editor | 需要 UI 回执 | ✅ 交互闭环存在 | B16/B48/B67；超时、非法回复和过大请求不能留下永久 pending |
| notify / setStatus / setWidget / setTitle / set_editor_text | fire-and-forget | ✅ 通用通道，无插件专属代码 | **setStatus 不需要回执**；B36 的状态缓存尚未按 session 隔离 |
| 自定义 TUI、footer/header/editor component | RPC 下不转发或 no-op | 🚫 不伪装支持 | 如需网页能力，应由上游提供标准通道 |
| 工具动态配置、原地树导航、reload | 未确认为标准 RPC 命令 | ⚠️ 不承诺支持 | TUI / SDK 方法存在不构成桥支持证据 |
| PTY / 文件 / Git | 桥独立职责 | ✅ 已接线，⚠️ 边界待修 | 不是 Pi RPC 的安全沙箱 |
| 模型配置、供应商 discover/test | 桥管理面 | ⚠️ 原始 JSON 编辑已接线 | 凭据恢复、写入与重定向待修；models.dev `config.catalog` 尚未接 UI |
| relay / 主动隧道 | 桥独立职责 | ⚠️ 后端存在，❌ 云端 UI 未接通 | B19–B24/B35/B54 等；不作为可交付云部署声明 |

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

✅ 2026-09-27 审查结束时：`go vet ./...`、`go test -race ./...` 通过；UI 72 项 Vitest、typecheck、build、check 通过，首屏静态依赖闭包 gzip 36.63 KiB / 40 KiB。既有浏览器验收覆盖普通发送、分页、扩展确认、终端关闭、移动布局等路径。

⚠️ 这些通过项不是对审查缺陷的否定。跨连接重复命令、重放窗口、超大响应、身份变更和迟到响应等边界已有额外反例；修复前仍为未解决。非 Linux 目前还存在 PTY 编译失败，不能写成“可构建但显式拒绝”。历史 SIGKILL 冒烟只覆盖直接 Pi 进程，不能证明子孙进程全部回收。

⚠️ Pi Web checkout 缺少完整依赖，本轮对照以源码为证据，不据其失败测试推断产品质量。没有进行公网 relay、受管 cgroup、全文件系统组合或全部浏览器的完整认证。

## 范围决定

| 状态 | 能力 | 原因或后续 |
|---|---|---|
| 🚫 明确不做 | OAuth 设备码登录、供应商额度查询 | 用户只使用 API 凭据 |
| 🚫 明确不做 | 插件/技能远程安装、更新、搜索及任意 CLI 透传 | 远程执行面；包由本机 CLI 管理，网页只读清单与版本 |
| 🚫 不提供 | 任意会话正文写入、session.import | 会话持久化归 Pi |
| ⚠️ 暂缓 | Web Push | 当前无需推送链路；不是永远禁止 |
| ⚠️ 未立项 | PWA、版本检查/更新、PDF、Minimap、多语言、worktree 等 | 不能把未实现推断为用户明确不要；另行确定产品范围 |
