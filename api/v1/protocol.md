# Bridge Protocol v1

更新：2026-09-28。本页描述当前 v1 的入口与方法，并单列尚未落地的目标语义。以运行代码和 `capabilities` 为准；方案及验收见 [architecture.md](../../docs/architecture.md)，遗留问题见 [code-audit.md](../../docs/code-audit.md)。

## 1. 当前入口与鉴权

本地桥提供 HTTP 与 WS；relay/tunnel 后端存在，但 HTMX 云端整链路尚未接通。浏览器通过 Bearer 认证换取 HttpOnly、SameSite=Strict Cookie 后连接同源 WS；原生客户端可在 upgrade 发 Authorization。Host、Origin 与 token 是独立检查。桥侧不以 query 接受 token；**当前 relay 设备 tunnel 仍用 query token，属于 B24 待修项**。

| 入口 | 用途 |
|---|---|
| `GET /healthz` | 健康检查，不返回模型、路径或进程信息 |
| `POST /api/v1/auth` | Bearer 换 Cookie |
| `GET /api/v1/capabilities` | 版本、方法、能力、限额；限额来自运行时配置（终端数量/空闲秒数跟随 `--max-terminals`/`--terminal-idle`），不再有写死的阶段字段 |
| `GET /api/v1/sessions?limit=50&offset=0` | 磁盘会话目录，不启动 worker |
| `GET /api/v1/sessions/{id}/history?limit=50&before=ENTRY&leafId=LEAF` | 所选持久分支历史，不启动 worker |
| `GET /api/v1/ws` | 文本 JSON 命令/响应/事件 |
| `/`、`/assets/*`、`/ui/*` | 配置 `--ui-dir` 后的模板、资产和片段 |

历史页的 `historicalModel: {provider,id}` 为可选只读投影：从所选叶子的父链中的 `model_change` 或最近一次助手回复提取，不启动 Pi，不表示该模型现在可用。UI 历史片段携带同一标识供输入栏显示；翻页不会用旧页模型覆盖当前分支。`session.state.model` 始终为模型对象或 `null`；Pi 在缺少对应模型配置时可能返回 `unknown/unknown` 对象，客户端不得把它当成可设置、可发送的模型。恢复会话时应以 worker 的状态为准，发送前手选模型的意图不得被启动过程中的状态回读取代。

`session.fork` 成功返回 `{sessionId,text,persisted}`：`text` 是原用户消息，供新分支草稿编辑；`persisted` 表示磁盘索引是否已经找到新 JSONL。Pi 0.85.1 在新分支没有 assistant 记录时延迟写盘，因此 `persisted:false` 的新 ID 只在当前 worker 中存在，关闭 worker 前若没有首条 assistant 回复就不能恢复。UI 不得按新 ID 直接请求磁盘历史；已有文件的分支仍按普通历史加载。`GET /ui/sessions/{id}/history` 对仍有活跃 worker、但磁盘尚无该会话文件的 ID 返回空正文 `204` 与 `X-Session-Unsaved: 1`；真正不存在的 ID、非法叶子仍报错。桥不替 Pi 写入伪造的会话文件。

业务响应默认 no-store；内容哈希静态资产有独立缓存策略。反代部署由 `--public-origin` 显式声明对外来源（S09）：
声明后桥额外接受该来源的 Host 与 Origin，非环回监听也只在该开关下放行，且只接受私有/overlay 网段地址；
会话 Cookie 的 `Secure` 跟随该来源的 scheme，而不是跟随回源连接——反代终止 TLS 时 `r.TLS` 恒为 nil，
据此推断会把 https 页面判成 http。桥不信任任何 `X-Forwarded-*`：`scheme` 只来自 `r.TLS` 与已声明的来源。
WebSocket 的 origin 白名单与同一规则对齐，否则库层（`Origin.Host` 必须等于 `r.Host`）会先拒掉桥自身已允许的来源。

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

`data` 的形状按方法固定，由 `internal/transport/responses.go` 的具名类型定义、`responses_test.go` 的表格钉住。**同一形状的方法共用同一个类型**，想改必须一次改到全部：

| 形状 | 用到它的方法 |
|---|---|
| `{queued}` | `session.steer`、`session.follow_up`（只表示已排入队列，**不代表任务完成**） |
| `{accepted}` | `session.prompt`（同上） |
| `{enabled}` | `session.set_auto_compaction`、`session.set_auto_retry` |
| `{aborted}` | `session.abort_retry`、`session.abort_bash` |
| `{written}` | `config.models.write`、`terminal.input` |
| `{level}` | `session.set_thinking`、`session.cycle_thinking`（返回生效后的等级） |
| `{sessionId}` | `session.new`、`session.switch`、`session.clone` |
| `{sessionId,text,persisted}` | `session.fork`（见下文「分支与导出」：`persisted:false` 只在当前 worker 中存在） |
| `{models}` | `config.models.discover`（供应商返回）、`config.catalog`（models.dev） |
| `{providers,modelCount}` | `config.models`（`providers` 是脱敏后的用户文档子树，形状由用户的 models.json 决定） |
| `{id,name,provider}` / `{id,provider,thinkingLevel}` | `session.set_model` / `session.cycle_model`（后者把生效后的思考等级一并返回） |
| `{entries,leafId}` | `session.entries` |
| `{summary,firstKeptEntryId,tokensBefore,estimatedTokensAfter}` | `session.compact`（估算值，非精确计数） |
| `{entries,truncated}` | `files.list` |
| `{text,truncated,size}` | `files.read` |
| `{mime,data,size}` | `files.image`（base64） |
| `{diff,truncated}` | `git.diff` |
| `{text,truncated}` | `session.bash_output` |
| `{dialogs,ids}` | `session.pending_dialogs`（`dialogs` 是 Pi 的原始载荷，HTTP 端点据此渲染） |
| `{epoch,statuses}` | `session.ext_status`（插件状态行快照，按 worker 隔离；`epoch` 标识它属于哪一轮工作进程；无 worker 的会话直接拒绝） |
| `{clearedQueue}` | `session.abort`（原始 JSON，桥不解释队列结构） |
| `{stopped}` / `{resized}` / `{closed}` / `{renamed}` / `{answered}` / `{path}` / `{kind,mode}` / `{text}` / `{roots}` / `{terminals}` | 各自单一方法 |
| `{sessionId,trashed,path[,stoppedWorker]}` | `sessions.delete`；`stoppedWorker` 只在本次删除连带停掉了运行中的会话时出现 |

事件侧只列桥自建的两个载荷：`bridge.terminal_closed` 的 `{terminalId}` 与 `terminal.output` 的 `{terminalId,data}`。**Pi 的事件（`agent_start`、`message_update` 等）是原样透传的原始 JSON**，结构由 Pi 决定，桥不解释也不重写。

错误码：`invalid_request`、`unsupported_version`、`unsupported_method`、`invalid_params`、`not_found`、`conflict`、`busy`、`limit_exceeded`、`worker_not_running`、`worker_exited`、`timeout`、`outcome_unknown`、`pi_error`、`resync_required`、`internal`；接入层还包括 `unauthorized`、`host_denied`、`origin_denied`。错误不含正文/密钥。任何新增错误码必须同步 TS 类型和兼容协商。

错误对象**只有 `code` 与 `message` 两个字段**（已在回归里钉住）。桥侧可以给内部错误挂上原因（`protocol.Wrap`）方便排查，但原因不参与序列化：它可能带本机路径、上游原文或凭据，既不进 WS 响应，也不进片段提示（片段只渲染 `message`）。

判定一律以**最外层**错误码为准。桥内部用 `errors.Is/As` 回溯原因，但按码分支的地方——例如历史端点「活跃但未落盘」的 204、回执的可重试判定——只读最外层的 `code`；把内层错误码当结论会让真实故障被当成可恢复状态。

## 3. 当前方法清单

运行时 `capabilities.methods` 是是否实现的入口依据，但方法存在不代表没有 [审查缺陷](../../docs/code-audit.md)。

| 分组 | 方法 |
|---|---|
| 进程与会话 | `worker.list`、`session.start`、`session.state`、`session.prompt`、`session.abort`、`session.stop`、`session.subscribe`、`session.unsubscribe` |
| 排队 | `session.steer`、`session.follow_up`、`session.set_queue_mode` |
| 模型 | `session.models`、`session.set_model`、`session.cycle_model`、`session.thinking_levels`、`session.set_thinking`、`session.cycle_thinking` |
| 工具预设 | `session.start` 的 `toolPreset` 参数（`chat-only`/`read-only`/`default`/`full`），见第 5 节 |
| 压缩与重试 | `session.compact`、`session.set_auto_compaction`、`session.set_auto_retry`、`session.abort_retry` |
| 分支 | `session.new`、`session.switch`、`session.fork`、`session.clone`、`session.tree`、`session.fork_messages`、`session.entries` |
| bash | `session.bash`、`session.abort_bash`、`session.bash_output` |
| 扩展对话 | `session.ui_response`、`session.pending_dialogs`、`session.ext_status` |
| 终端 | `terminal.open`、`terminal.input`、`terminal.resize`、`terminal.close`、`terminal.list` |
| 文件与 Git | `files.list`、`files.index`、`files.stat`、`files.read`、`files.image`、`files.roots`、`git.status`、`git.diff` |
| 其他 | `session.stats`、`session.set_name`、`session.last_assistant`、`session.commands`、`session.export_html`、`sessions.search`、`sessions.delete` |

`session.export_html` 是**磁盘投影**：不要求活动 worker，也不调用 Pi 的 `export_html`——桥直接从 JSONL 生成自包含 HTML（无脚本、单文件 ≤ 64 MiB、写入用临时文件加改名，目录 32 个 / 256 MiB 上限）。Pi 自己的 `export_html` 仍被 `/ui/system` 与 `/ui/tools` 用来取 `systemPrompt`/`tools`。
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

**当前传输边界：** WS 出站缓冲 32 帧、tunnel 虚拟连接 64 帧，均额外限制待发送字节为 1 MiB、单帧 512 KiB。连接发送者短暂拥塞时有界等待；同一批 replay 共用 5 秒截止时间，超时返回 `resync_required` 并释放订阅，而不是仅因队列瞬时满就断连接。worker 自身的订阅队列仍有独立条数/字节预算，高速事件可能触发 `bridge.subscription_closed`；UI 应核对 `session.state` 与磁盘历史，不得把断开的实时片段冒充完整。订阅确认的身份数据仍待客户端显式消费，不承诺无损跨连接恢复。

## 5. 历史与资源

```json
{"sessionId":"...","leafId":"last-durable-entry","leafSource":"disk","entries":[],"oldestEntryId":null,"hasMore":false}
```

- entries 祖先到后代排列；before 排除边界且应属于所选 leaf 祖先链。默认磁盘叶子不声称是活跃 Pi 内存导航位置。
- 原始条目 limit 不是 UI 消息数；回合对齐可额外取记录，但仍受硬字节/条目上限。
- ID 通过受管根下的文件头索引解析；列表/普通历史只读、不启动 Pi，不重写/迁移 JSONL。
- 目标校验包括完整坏行、重复 ID、断链、循环；仅忽略尾部半行。B12/B13 的缓存身份和快路径结构校验已修，剩余边界见审计台账。
- `sessions.search` 返回 `{matches:[{sessionId,entryId,title,cwd,role,snippet,timestamp}],scanned,truncated}`；标题优先取本次扫描中的 `session_info`，否则退回首条用户消息，不为匹配项额外重扫文件。UI 用 `entryId` 加载截至命中位置的只读历史，并明确提示可返回当前分支。搜索还需受命中、访问文件、目录、字节、时间、并发上限约束；B28/B52 已修，B43/B72 仍待处理。
- 删除前协调活跃 worker、trash 存在但失败不降级永久删除的 B08/B62 已修，具体 force 行为见审计台账。

### 工具预设与思考强度

`session.start` 接受可选 `toolPreset`，取值 `chat-only`/`read-only`/`default`/`full`，未知值必须报 `invalid_params`，不静默回落默认。预设只在拉起 Pi 进程时通过 CLI 生效，运行中切换需要先 `session.stop` 再以新预设 `session.start`；`worker.list`/启动响应回带 `toolPreset`，管理器按会话记住最近一次选择，空闲回收后重启沿用。

预设到 Pi CLI 的映射与 Pi Web 的 `lib/tool-presets.ts` 对齐：`default` 不加参数（Pi 默认 read/bash/edit/write，扩展工具可用），`chat-only` 用 `--no-tools`，`read-only` 用 `--exclude-tools bash,edit,write`。**`full` 只能用 `--tools` 白名单启用 grep/find/ls，而 Pi 的白名单同时作用于扩展工具，因此 `full` 会禁用 magic-context 等扩展工具**——这是上游限制，UI 必须明示，不能假装与 Pi Web 完全一致。

`session.thinking_levels` 返回当前模型支持的等级（来自 Pi 的 `get_available_thinking_levels`，无模型时仅 `off`）。UI 的「自动」不是 Pi 等级，而是“不发送 `session.set_thinking`”的语义，由 Pi 按 settings 的 `defaultThinkingLevel` 与模型能力决定；Pi 没有“未设置”读回字段，因此自动/等级选择只能按会话在本地记忆，界面必须标注这是本机偏好而非 Pi 实时状态。

`session.stats` 的 `contextUsage` 形如 `{tokens,contextWindow,percent}`：只有 worker 持有带 contextWindow 的模型时才非空；压缩后尚未产生新的 assistant 回复时 Pi 返回 `tokens/percent` 为 `null`，UI 必须显示未知而不是 0。magic-context 另有一条自己的 `mc: ...` 扩展状态行（经 `setStatus` 到达 `#ext-status-slot`），两者数据来源不同，不合并、不互相推导。

### 惰性内容

历史投影只带块位置，不带正文/base64：

```json
{"lazy":[{"blockIndex":0,"kind":"thinking"},{"blockIndex":1,"kind":"image"}]}
```

```text
GET /ui/sessions/{id}/lazy?kind=thinking&entryId=ENTRY&blockIndex=0
GET /ui/sessions/{id}/lazy?kind=thinking&format=html&entryId=ENTRY&blockIndex=0
GET /ui/sessions/{id}/lazy?kind=tool-image&entryId=ENTRY&blockIndex=1
GET /ui/sessions/{id}/lazy?kind=user-image&entryId=USER_ENTRY&blockIndex=1
```

thinking 返回 JSON；tool-image 与 user-image 分别只允许 toolResult/user 角色并返回图片字节。索引/格式/字节均校验，SVG 不作为受支持的图片。用户附件只在历史 HTML 中生成定位按钮，不内嵌 base64，点击时才取原图；定位必须保留每个真实 entryId，不能拿整轮最后 assistant 代替（B11）。Pi 的 `stopReason:"error"` 可能以空内容 assistant 写盘而不让 prompt RPC 抛错，历史与实时预览必须显示失败；只投影已知的安全类别（如 HTTP 402 余额不足、429 限流），不回显上游错误正文或 request_id。目标使用同一已验证文件索引读取正文，不缓存整份内容（B38）。

### 服务端渲染片段

「已有数据 → HTML」一律由桥渲染，前端只用 `hx-*` 属性与自定义事件触发刷新，不再用 JS 拼列表：

```text
GET /ui/sessions                        会话侧栏
GET /ui/search?q=KEY                    搜索结果（含 title/cwd/entryId）
GET /ui/sessions/{id}/history           历史回合
GET /ui/files?path=DIR                  文件浏览
GET /ui/git-status?path=DIR             Git 变更列表
GET /ui/diff?path=DIR                   差异
GET /ui/branch?sessionId=ID&leafId=…    分支树 + 可分支消息
GET /ui/models、/ui/packages、/ui/extensions/*
GET /ui/dirs?path=PATH
GET /ui/mc?kind=memories&offset=0&limit=50
GET /ui/mc/content?kind=memories&id=1
POST /ui/models/discover、/ui/models/test
```

约定：

- 参数由模板里的隐藏输入提供，前端只写值并触发事件（`files-refresh`、`git-status-refresh`、`diff-refresh`、`search-refresh`、`branch-refresh`、`dialogs-refresh`、`sessions-refresh`、`models-refresh`、`packages-refresh`），不拼 URL。
- 迟到响应的归属判定放在 `htmx:beforeSwap`（交换之前），按响应 URL 里的参数与当前状态比对，不靠调用时的闭包快照。
- 片段不得含 `<script>`、不得含完整文档结构、不得含动态内联 `style`（受 CSP 约束）。层级缩进用 `aria-level` + 静态 CSS。
- `/ui/branch` 需要活动 worker：`get_tree` 是 Pi 进程内命令，没有纯磁盘等价物；未启动时返回 `409 worker_not_running`，不静默返回空树。

留浏览器侧的只有「转瞬即逝的交互状态」：按键补全与斜杠菜单、滚动锚定、WS 流式增量、textarea 自适应高度、xterm、未上传的本地附件缩略图、以及 markdown/高亮/KaTeX/ANSI 渲染管线。判据是「这份数据在服务端有没有权威版本」。

### 系统提示词与工具定义

Pi 的 RPC **没有**暴露这两项的命令（`rpc-mode` 的命令表里 `get_state` 只给模型/思考等级/流式状态/会话文件，没有 `systemPrompt` 也没有 `tools`）。唯一带着它们的出口是 `export_html`：它把 `AgentState` 的 `systemPrompt` 与 `tools` 一并写进导出 HTML 内嵌的 `<script id="session-data">`（base64 编码的 JSON，字段 `header/entries/leafId/systemPrompt/tools`）。

```text
GET /ui/system?sessionId=ID     系统提示词（含按工具集重建的规则段与扩展追加部分）
GET /ui/tools?sessionId=ID      当前实际暴露给模型的工具：name/description/parameters
```

- 桥按需导出一次到**自己的临时目录**（不是导出目录，避免内部产物被 `/ui/exports` 下载），读回后立即删除。
- 结果按 `(sessionId, epoch)` 缓存 120 秒、上限 8 条。epoch 变化（fork/clone/切换身份）即失效；不缓存就等于每开一次面板都重导一次完整会话。
- `tools` 是 `AgentState.tools`，也就是**实际生效**的工具集合，受启动时的工具预设与扩展追加影响，不是"全部可用工具目录"。
- 两个端点都需要活动 worker（`systemPrompt`/`tools` 只存在于活的 Pi 进程里，JSONL 不含它们）。导出超过 64 MiB（`limit_exceeded`）或模板里找不到数据块时明确失败，不静默返回空面板。

### 顶栏面板的呈现方式

面板是**贴顶栏下沿的下拉浮层**（绝对定位覆盖对话区），与 Pi Web 的 fixed 下拉一致，不是把对话内容压下去。浮层只需要知道顶栏高度：CSS 给出断点默认值（`--topbar-h`），`layout.ts` 用 `ResizeObserver` 校正实际值（状态文字变长、按钮换行都会让真实高度偏离默认值）。

### 片段端点的状态码约定

htmx 换入的片段端点（`/ui/sessions`、`/ui/search`、`/ui/models`、`/ui/files`、`/ui/git-status`、`/ui/diff`、`/ui/branch`、`/ui/system`、`/ui/tools`、`/ui/stats`、`/ui/extensions/*`）**一律返回 `200` + 一段可读 HTML**，包括「worker 未启动」这类前置状态：

- 理由是 htmx 默认不交换 4xx/5xx 响应。若按错误码返回，面板会停在旧内容上且没有任何解释——这是真机复现过的现象（未启动 worker 时点「会话信息」，请求确实发出，界面却一直显示占位文字）。
- 与其在前端用 JS 强制交换错误响应，不如让端点把状态当作内容渲染（`internal/presentation` 的 `RenderNote`）：由服务端决定用户看到什么。
- **真实状态码例外**：`/ui/file-text`、`/ui/file-image`、`/ui/sessions/{id}/lazy` 保留读取/参数失败的真实码。lazy 默认仍返回 JSON/图片，思考的 `format=html` 返回转义后的片段，错误由前端统一提示。`/ui/exports/*` 是浏览器导航。外壳 `/` 与 `/assets/*` 同理：缓存层按状态码判断，且资源名含内容哈希、带 `immutable` 缓存语义。

新增片段契约（2026-09-30）：模型发现/测试 POST 与 WS 调用同用 management 服务、出站策略和全局操作槽；表单最大 64 KiB，字段为 `baseUrl/api/apiKey/headers`（头部一行一个 `名称: 值`）。鉴权与 Host/Origin 检查先于路由，不执行远程命令型密钥、不向供应商发送 `***` 占位值。结果由 Go 模板转义。模型数值上限字段必须是安全整数范围内的正整数；页面发送整份草稿快照，桥仍按原身份恢复秘密，不提供配置 CAS。

记忆追加页用 `append=1`：返回 `#mc-rows` 的 OOB 追加和替换的 `#mc-more`，末页不再给按钮。正文入口限定分区白名单、正整数 ID、最多 65536 字符；只读、不缓存，不返回已归档的 memories 正文。目录片段的成功路径由 OOB 字段及成功标记共同确认，错误片段不承诺成功浏览。
- **四处例外是状态信号，不是片段语义**（调用方在 `htmx:beforeSwap` 里读它们，见 `src/modules/workbench.ts`）：`/ui/sessions/{id}/history` 的 `204 + X-Session-Unsaved`（新分支尚未落盘）、`/ui/extensions/dialog/{id}` 的 `204`（对话已被回答，移除占位）、以及这两处「明确非法参数」回 `400`（本仓前端不可能发出这类请求）。
- 反例：`/ui/sessions/{id}/history` 对「会话已不存在」这类**状态**失败曾回 `400`，于是 htmx 不交换、界面停在旧历史上且没有解释。现已按片段约定改为 `200 + 可读提示`。判断标准是：**失败原因是可向用户解释的状态，还是程序性故障**——前者当内容渲染，后者（例如模板缺失）保留 `500`，让日志里看得见。

### 会话详情

```text
GET /ui/stats?sessionId=ID
```

布局对齐 Pi Web 的会话弹层：三栏网格 `minmax(300px,1.7fr) minmax(140px,.55fr) minmax(190px,.75fr)`（≤760px 收成单栏），左栏「会话信息 / 项目信息」，中栏消息计数，右栏 Token 与用量右对齐（对应 Pi Web 的 compact 变体，行距 4px）。行文案与 Pi Web 的 zh-CN 文案逐条对齐：会话文件 / ID / 项目目录 / Git 分支 / 用户 / 助手 / 工具调用 / 工具结果 / 总计 / 平均缓存命中率。

可复制的行（会话文件、ID、项目目录、Git 分支）带复制按钮，`data-copy-value` / `data-copy-label` 由桥输出，浏览器只负责写入剪贴板。

**未包含「活跃时长」**：Pi Web 由前端从内存里的会话条目按时间戳累加得出，而 `get_session_stats` 不含该值。桥要给出它就得完整扫一遍 JSONL——那正是 B37（会话标题首读扫全文件）被判定为问题的模式，因此不在打开面板时重扫。需要的话应在索引构建时累加，与 B37 一并处理。

**顶栏面板没有标题行**：Pi Web 的这几个面板都是「内容直接铺满」，关闭靠再点一次工具栏按钮。因此桥渲染的片段里不含标题；壳层只放一个绝对定位的 ×（不占垂直空间，读屏/键盘用户可用），并支持 Escape 关闭。模型、思考强度、工具预设不在这块面板里重复——输入栏各有一个控件。工具面板同样是两栏（左栏工具名列表、右栏该工具的定义），所有工具的详情一次性渲染进 HTML，切换选中项只是显示/隐藏，不再发请求——否则每点一次都要重新导出一次会话快照。

数字全部来自 Pi 的 `get_session_stats`（`userMessages/assistantMessages/toolCalls/toolResults/totalMessages`、`tokens.{input,output,cacheRead,cacheWrite,total}`、`cost`、`contextUsage`），桥只做分类与格式化。缓存读写与缓存命中率只在实际发生时显示。`get_session_stats` 会因失败回合整体报错，那时仍输出会话、项目、运行三组事实——面板至少该告诉你「这是哪个会话」。

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
