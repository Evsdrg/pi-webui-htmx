# Bridge Protocol v1

更新：2026-09-27。本页保留 **当前 v1 的入口与方法**，另列已审查的契约缺口和目标语义。本轮没有改产品实现、`protocolVersion` 或 manifest；新字段/方法只有实现并协商后才可调用。方案及验收见 [architecture.md](../../docs/architecture.md)，问题见 [code-audit.md](../../docs/code-audit.md)。

## 1. 当前入口与鉴权

本地桥提供 HTTP 与 WS；relay/tunnel 后端存在，但 HTMX 云端整链路尚未接通。浏览器通过 Bearer 认证换取 HttpOnly、SameSite=Strict Cookie 后连接同源 WS；原生客户端可在 upgrade 发 Authorization。Host、Origin 与 token 是独立检查。桥侧不以 query 接受 token；**当前 relay 设备 tunnel 仍用 query token，属于 B24 待修项**。

| 入口 | 用途 |
|---|---|
| `GET /healthz` | 健康检查，不返回模型、路径或进程信息 |
| `POST /api/v1/auth` | Bearer 换 Cookie |
| `GET /api/v1/capabilities` | 版本、方法、能力、限额；当前 phase/部分限额仍有 B80 硬编码 |
| `GET /api/v1/sessions?limit=50&offset=0` | 磁盘会话目录，不启动 worker |
| `GET /api/v1/sessions/{id}/history?limit=50&before=ENTRY&leafId=LEAF` | 所选持久分支历史，不启动 worker |
| `GET /api/v1/ws` | 文本 JSON 命令/响应/事件 |
| `/`、`/assets/*`、`/ui/*` | 配置 `--ui-dir` 后的模板、资产和片段 |

业务响应默认 no-store；内容哈希静态资产有独立缓存策略。反代信任与 Secure Cookie 的目标规则见架构 S09，当前不能据文档假设 HTTPS 回源路径已修复。

## 2. 封装

```json
{"version":1,"kind":"command","requestId":"req-1","sessionId":"pi-session-id","method":"session.prompt","params":{"text":"Hello"}}
```

- version 必须为 1；客户端 kind 为 command，服务端为 response/event/control。
- requestId 必填且受长度限制；客户端对每次新的用户操作生成新 ID。
- sessionId 是受管 Pi session ID，不是任意路径；创建类调用可省略。
- 方法/参数使用 allowlist；不存在的能力返回 unsupported_method，不开放任意 Pi RPC。

```json
{"version":1,"kind":"response","requestId":"req-1","ok":true,"data":{"accepted":true}}
{"version":1,"kind":"response","requestId":"req-1","ok":false,"error":{"code":"worker_not_running","message":"请先显式启动会话"}}
```

错误码：`invalid_request`、`unsupported_version`、`unsupported_method`、`invalid_params`、`not_found`、`conflict`、`busy`、`limit_exceeded`、`worker_not_running`、`worker_exited`、`timeout`、`outcome_unknown`、`pi_error`、`resync_required`、`internal`；接入层还包括 `unauthorized`、`host_denied`、`origin_denied`。错误不含正文/密钥。任何新增错误码必须同步 TS 类型和兼容协商。

## 3. 当前方法清单

运行时 `capabilities.methods` 是是否实现的入口依据，但方法存在不代表没有 [审查缺陷](../../docs/code-audit.md)。

| 分组 | 方法 |
|---|---|
| 进程与会话 | `worker.list`、`session.start`、`session.state`、`session.prompt`、`session.abort`、`session.stop`、`session.subscribe`、`session.unsubscribe` |
| 排队 | `session.steer`、`session.follow_up`、`session.set_queue_mode` |
| 模型 | `session.models`、`session.set_model`、`session.cycle_model`、`session.thinking_levels`、`session.set_thinking`、`session.cycle_thinking` |
| 压缩与重试 | `session.compact`、`session.set_auto_compaction`、`session.set_auto_retry`、`session.abort_retry` |
| 分支 | `session.new`、`session.switch`、`session.fork`、`session.clone`、`session.tree`、`session.fork_messages`、`session.entries` |
| bash | `session.bash`、`session.abort_bash`、`session.bash_output` |
| 扩展对话 | `session.ui_response`、`session.pending_dialogs` |
| 终端 | `terminal.open`、`terminal.input`、`terminal.resize`、`terminal.close`、`terminal.list` |
| 文件与 Git | `files.list`、`files.index`、`files.stat`、`files.read`、`files.image`、`files.roots`、`git.status`、`git.diff` |
| 其他 | `session.stats`、`session.set_name`、`session.last_assistant`、`session.commands`、`session.export_html`、`sessions.search`、`sessions.delete` |
| 模型配置 | `config.models`、`config.models.raw`、`config.models.write`、`config.models.discover`、`config.models.test`、`config.catalog` |
| 资源清单 | `config.packages`、`config.settings`、`config.trust` |

共同边界：cwd 必须在允许根内；恢复时匹配会话头。网络不能选择 Pi 可执行文件/附加参数/任意环境。文件/Git/PTY 有工作区边界，但不构成 Pi 工具沙箱。settings/trust/packages 只读；插件安装、更新、卸载不提供网络接口。

### 排队的两种维度

- 单条消息的目标：`prompt.params.streamingBehavior` 为 `steer` 或 `followUp`。
- 投递设置：`session.set_queue_mode` 使用 `{kind:"steering"|"followUp", mode:"all"|"one-at-a-time"}`。
- `steeringMode/followUpMode` 不能告诉 UI 用户下一条消息想发到哪个队列。当前 UI 把 steer 传作 kind 且混淆两者，B03/U11 尚未修。

### 配置与秘密

当前 models 配置遵循 Pi 数组 schema，`api` 是协议标识，`baseUrl` 是 URL。P1 已实现：所有自定义头部值默认脱敏，模型按 ID 恢复秘密，摘要按数组统计并限制总数，随机独占临时文件可靠替换。v1 的 `***` 仅表示保留已有值，无来源或歧义时返回 invalid_params；不能用它表示新的字面量密钥。读取损坏时，只能以明确新值修复，不能猜测秘密。

当前禁止网页新增/修改 Pi 的 `!command` 凭据；原身份的本机表达式可保留，`$!` 是普通转义。discover/test 不解析或执行表达式，专用 client 拒绝重定向；超限正文返回 limit_exceeded，上游错误仅透出 HTTP 状态。现有写锁只协调本 Config 实例，不声称已防止外部 CLI 或旧浏览器草稿覆盖。

目标 S04 尚余：revision 检查、明确 keep/replace/remove 秘密操作及其 UI。新增 revision/秘密操作形状在 v2 配套发布，不能静默替换当前 v1 raw 响应。

## 4. 事件与恢复

```json
{"version":1,"kind":"event","sessionId":"...","streamId":"worker","epoch":"generation","seq":42,"event":"pi.event","data":{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}}
```

Pi 原事件通过 `pi.event` 传递并绑定基线版本；不是跨 agent 通用协议。按 contentIndex 组装 delta，message_end.message 替换成权威值；agent_end 不等于 settled。worker 状态使用 bridge.worker_state。

**当前存在：** 有界订阅、epoch/seq、replay、持久回执和 unknown 状态。**当前缺口：** B04/B05/B30/B31/B47/B58/B65 等仍可造成重复派发、事件窗口丢失或恢复错误。

目标 S01/S02：

1. 须持久化的变更按主体/设备/requestId 加 method/session/params 指纹，跨连接原子 claim，派发前可靠写 intent、终态后写 receipt。只读/连接操作、PTY 临时输入、控制与对话回复使用整体规划的独立策略，不逐按键 Sync，不让日志故障挡住取消。
2. 重启仅有 intent 则 unknown，不重发。只能保证有效保留期内至多派发一次，不承诺 exactly-once；保留窗口和 degraded 状态必须可见。
3. 同 ID 不同指纹 conflict；同 ID pending 等待或返回 pending，不能启动第二次执行。记录已淘汰时“查无记录”不等于未执行。
4. replay 快照与 live 注册原子化；确认给出 epoch/fence，依次输出 replay 与后续事件。
5. worker 启动和 session rebind 都换 epoch；UI 只接受当前连接确认的 epoch，不被迟到旧事件改变。
6. omitted/resync 立即重读持久历史并标记 live 缺口；不伪造丢失 delta。
7. 已受理任务独立于连接寿命；未知结果客户端只对账，不自动重发 prompt/bash 等变更命令。

## 5. 历史与资源

```json
{"sessionId":"...","leafId":"last-durable-entry","leafSource":"disk","entries":[],"oldestEntryId":null,"hasMore":false}
```

- entries 祖先到后代排列；before 排除边界且应属于所选 leaf 祖先链。默认磁盘叶子不声称是活跃 Pi 内存导航位置。
- 原始条目 limit 不是 UI 消息数；回合对齐可额外取记录，但仍受硬字节/条目上限。
- ID 通过受管根下的文件头索引解析；列表/普通历史只读、不启动 Pi，不重写/迁移 JSONL。
- 目标校验包括完整坏行、重复 ID、断链、循环；仅忽略尾部半行。B12/B13 是当前缓存/快速扫描违例。
- 搜索应受命中、访问文件、目录、字节、时间、并发上限约束；B28/B43/B52/B72 未完成前不宣称所有限制已强制执行。
- 删除目标是拒绝活跃 writer、失败不永久降级；当前 B08/B62 尚未修。

### 惰性内容

历史投影只带块位置，不带正文/base64：

```json
{"lazy":[{"blockIndex":0,"kind":"thinking"},{"blockIndex":1,"kind":"image"}]}
```

```text
GET /ui/sessions/{id}/lazy?kind=thinking&entryId=ENTRY&blockIndex=0
GET /ui/sessions/{id}/lazy?kind=tool-image&entryId=ENTRY&blockIndex=1
```

thinking 返回 JSON，tool-image 返回图片字节；索引/格式/字节均校验，SVG 不作为受支持的图片。定位必须保留每个真实 entryId，不能拿整轮最后 assistant 代替（B11）。目标使用同一已验证文件索引读取正文，不缓存整份内容（B38）。

### 工作区文件

- `files.index` 无 query 返回 `{files:[...],truncated}`，有 query 返回 `{matches:[{path,isDir}],truncated}`；这是同方法的两种显式模式。
- Git 索引已使用 NUL 增量分帧及硬字节/条目上限，截断状态进入缓存；仅非仓库/未安装 Git 才退回 walk，拒绝和取消不静默降级。walk 的 B27 仍待修复。
- `git.status` 返回 `{branch,clean,files,truncated}`，文件项含 `{status,path}`，重命名可含 `from`。64 KiB 原始输出和文件条数限额保证完整记录并预留转义预算，截断时不宣称 clean，UI 必须提示。
- Git 查询固定可执行文件并清理 Git 环境，禁 fsmonitor/external diff/textconv、pager、懒获取和可选锁；配置转换过滤器时明确拒绝。工作树与 Git 元数据目录必须在授权根内，需要支持 `--no-lazy-fetch` 的 Git。它不是任意 Git 命令或恶意本机进程的 OS 沙箱。
- **当前 `files.image` WS 返回 base64，`GET /ui/file-image?path=...` 才返回二进制。** `files.read` 拒绝二进制；图片按魔数检测。
- 目标 S06：大内容走 HTTP 范围/分页/下载，WS 只传引用；保留小 v1 请求时先检查序列化长度，超限显式报错而非取消整个连接。

## 6. 限额、能力与可观测性

当前默认：WS 请求 1 MiB、响应 512 KiB；Pi JSONL 8 MiB；单事件 256 KiB。这些边界不同，不能用一个“支持 8 MiB 图片”的 UI 数字代替完整链路计算。详细错配见 [communication.md](../../docs/communication.md)。

目标由同一配置/方法描述生成：实际限额、方法期限、只读/副作用分类、存储健康与保留窗口、可用大内容接口。每连接/全局/每主体分别限额，HTTP 与 tunnel 不得绕开配额。

metrics 需鉴权，使用有界方法/错误标签；日志只记录关联 ID、方法、状态、耗时及类别，不记录正文、URL token、apiKey 或秘密 header。

## 7. relay 与版本演进

当前方向信封为 relay→bridge 的 `{from,data}` 与 bridge→relay 的 `{to,data}`；缺 to 不广播。同 clientId 重连应替换旧连接，但 B57 说明死 pump 复用仍需修。relay 接触转发明文，不应落盘或日志记录秘密；不能称为端到端加密。

目标 S12 在同源设备前缀下转发受控 HTTP 资源与 WS 命令，补齐 HTMX 云链路；分块/取消/credit/鉴权都属于新传输能力，当前接口不能假装已经支持。

本次完整修复按 [整体规划](../../docs/repair-plan.md) 的 P6 集中升级到 v2，配套更新桥/UI/manifest/TS/工具；P1–P5 中可保持形状的修复继续按当前 v1 验证。当前版本仍为 v1；各批修复状态以审查台账为准。独立可选能力仍可协商，但不能在 v1 下暗改订阅、重复结果、配置秘密和资源引用语义。

新版写入口上线后，旧写协议明确拒绝并提示升级；不保留旧盲写/去重路径作为回退。必要旧读取适配必须有期限并复用相同业务服务。Journal 的离线迁移、保守导入与不可恢复旧快照的回滚边界见整体规划。本文目标说明不是启用新能力的依据。
