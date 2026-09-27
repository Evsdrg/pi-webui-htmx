# 修复基线：入口与方法分类

P0 清单，基于当前 62 个方法及 HTTP 路由。以下分类是 P2 共用执行器的设计输入，**不代表当前已经按此执行限流/持久化**。`ui_contract_test.go` 核对本表、Go 注册表、配套 UI 类型与模板；这里不替代行为测试。

## 方法分类

| 方法 | 执行类别 | 目标来源 | 预算/期限族 |
|---|---|---|---|
| `worker.list`、`terminal.list`、`config.models`、`config.models.raw`、`config.settings`、`config.trust` | 本地只读 | 认证设备/配置域 | 元数据读取 |
| `session.state`、`session.models`、`session.thinking_levels`、`session.stats`、`session.last_assistant`、`session.commands`、`session.tree`、`session.fork_messages`、`session.entries`、`session.bash_output`、`session.pending_dialogs` | 当前 worker 读取 | request.sessionId → 固定 worker 实例 | RPC 读取；tree/entries/output 后续改有界数据通道 |
| `sessions.search`、`files.list`、`files.index`、`files.stat`、`files.read`、`files.image`、`files.roots`、`git.status`、`git.diff` | 文件/目录只读 | 受管目录或 params.path | 磁盘/Git，字节、目录、匹配和时间预算 |
| `config.models.discover`、`config.models.test`、`config.catalog`、`config.packages` | 联网读取 | 审核后的 origin/配置域 | 出站并发、总时长和响应字节 |
| `session.subscribe`、`session.unsubscribe` | 连接资源 | peer + 固定 worker/订阅代次 | 订阅数、重放及 live 字节 |
| `session.start` | 生命周期申请 | sessionId 恢复或 cwd 新建意图 | 启动数/期限；新建需保留请求到结果身份的关联 |
| `session.prompt`、`session.steer`、`session.follow_up`、`session.bash` | 一次性执行 | 固定 worker | 持久 intent；接受与完成分开 |
| `session.set_model`、`session.cycle_model`、`session.set_thinking`、`session.cycle_thinking`、`session.set_queue_mode`、`session.compact`、`session.set_auto_compaction`、`session.set_auto_retry`、`session.set_name` | 状态变更 | 固定 worker | 持久 intent；compact 独立长操作期限 |
| `session.new`、`session.switch`、`session.fork`、`session.clone` | 身份事务 | 源 worker + 目标预留/transition | 持久 intent、身份核验与事件边界 |
| `config.models.write` | 持久配置变更 | 设备/配置 revision | intent、配置锁、原子写入 |
| `sessions.delete` | 持久删除 | params.sessionId | 与 start 共用预留，不按 request.sessionId 猜目标 |
| `terminal.open` | 进程资源创建 | 受管 cwd/固定 shell | intent、进程配额/启动期限 |
| `terminal.input`、`terminal.resize` | 临时输入/最新状态 | 固定 terminal 实例 | 有界输入；不逐批 Sync，不重发输入 |
| `session.abort`、`session.stop`、`session.abort_retry`、`session.abort_bash`、`terminal.close` | 控制 | 已捕获 worker/terminal 实例 | 有界保留容量；不等待普通写队列 |
| `session.ui_response` | 一次性对话回复 | worker + epoch/transition + dialogId | dialog claim、控制队列与 stdin 写入结果 |
| `session.export_html` | 有界产物生成 | 当前 worker；目标迁到只读文件投影 | 总磁盘、并发、期限、TTL；不将产物视为会话正文 |

只读仍会消耗资源或向外发请求，不能跳过鉴权与预算。新建与恢复的 start 不能用一个幂等标签概括。具体策略落地时以共用 MethodSpec 为唯一执行事实来源；此表保留说明，静态集合检查防止漏列。

## HTTP 入口

| 当前入口 | 服务归属 | 必须共用的边界 |
|---|---|---|
| GET `/healthz` | 健康检查 | 不读取秘密/正文 |
| POST `/api/v1/auth` | 认证 | Host/Origin、认证预算；不进入普通业务 Journal |
| GET `/api/v1/capabilities`、`/api/v1/metrics` | 设备元数据 | 鉴权、实际配置、固定指标标签 |
| GET `/api/v1/sessions`、`/api/v1/sessions/{id}/history` | Store 只读 | 目录/字节/并发/取消；0 worker |
| GET `/`、`/assets/*` | UI 外壳/资产 | 同一构建代次、缓存/压缩、路径限制 |
| GET `/ui/sessions`、`/ui/sessions/{id}/history`、`/ui/sessions/{id}/lazy` | Store + presentation | 与 JSON API 同一读取服务，不能单独绕过扫描预算 |
| GET `/ui/models`、`/ui/packages` | management + presentation | 配置域/出站读取预算；packages 不是本地廉价操作 |
| GET `/ui/files`、`/ui/file-image`、`/ui/diff` | workspace + presentation | 文件沙箱、实际 reader 上限、Git runner |
| GET `/ui/extensions/status`、`/ui/extensions/dialog/{id}`、`/ui/extensions/dialogs` | worker 展示投影 | worker/session/epoch 隔离，不因渲染消耗 pending |
| POST `/ui/sessions/{id}/ui-response` | 对话服务 | 与 WS session.ui_response 相同 claim/验证/写入确认 |
| GET `/ui/exports/{name}` | 受控产物下载 | 授权、名称限制、完成/取消/TTL 清理 |
| GET `/api/v1/ws` | 本地接入 | 升级鉴权、每连接/全局准入与唯一 writer |
| tunnel 的虚拟连接 | 隧道接入 | 可信主体/设备，复用以上服务；当前尚无完整 HTTP tunnel |

## 可复现验证

```bash
PI_WEBUI_DIR=/absolute/path/to/pi-webui-htmx scripts/verify-pair.sh
```

脚本记录两仓实际 HEAD/工作树，使用锁文件安装，执行 UI 单测/类型/构建/契约及带真实 UI 包的 Go vet/race。缺少 UI 路径直接失败。两仓独立 CI 已配置；桥 CI 未提供 UI 时明确跳过跨仓测试，不算联测通过。当前没有 Git remote，未配置未知来源的跨仓下载，也未声称托管 CI 已运行。

P0 本地联测：72 项前端测试通过，首屏 gzip 36.64 KiB / 40 KiB，Go 16 个测试包通过。假 Pi 独立构建测试已反向验证：临时恢复固定输出路径会失败，已恢复正确实现。
