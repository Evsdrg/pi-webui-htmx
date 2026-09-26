# Bridge Protocol v1

状态：v1 契约。当前已实现 A–E 阶段全部命令；实现能力以 `GET /api/v1/capabilities` 的
`methods` 为准，未列入的方法一律返回 `unsupported_method`，不得伪造成功。

## 传输和认证

本地桥提供 HTTP + WebSocket；云隧道是后续传输适配。A 阶段仅绑定 loopback，使用显式 Bearer token。浏览器通过认证 POST 换取 HttpOnly、SameSite=Strict cookie，再连接同源 WS；原生客户端可在 WS upgrade 发送 Authorization。URL query 不接收 token。Origin 与 Host 检查独立于 token。

- `GET /healthz`：只返回健康状态，不泄露路径、模型或进程信息。
- `POST /api/v1/auth`：Authorization: Bearer token；设置会话 cookie。
- `GET /api/v1/capabilities`：版本、方法、功能缺口、限额。
- `GET /api/v1/sessions?limit=50&offset=0`：磁盘会话目录，不启动 worker。
- `GET /api/v1/sessions/{id}/history?limit=50&before=ENTRY&leafId=LEAF`：所选分支历史，不启动 worker。
- `GET /api/v1/ws`：升级 WS，文本 JSON 帧。

默认请求/响应不缓存（Cache-Control: no-store）。反向代理模式、云端身份与配对属于后续阶段，A 阶段不接受任意可信代理头。

## 封装

```json
{"version":1,"kind":"command","requestId":"req-1","sessionId":"pi-session-id","method":"session.prompt","params":{"text":"Hello"}}
```

- version：必须为 1。
- kind：客户端命令为 command；服务端为 response/event/control。
- requestId：每个命令必须非空、长度受限；连接内不得重用。
- sessionId：Pi session ID，不是任意路径；仅创建时省略。
- method/params：由 allowlist 和方法 schema 验证，不开放任意 Pi RPC 透传。

成功：

```json
{"version":1,"kind":"response","requestId":"req-1","ok":true,"data":{"accepted":true}}
```

失败：

```json
{"version":1,"kind":"response","requestId":"req-1","ok":false,"error":{"code":"worker_not_running","message":"Start the session explicitly"}}
```

错误码：`invalid_request`、`unsupported_version`、`unsupported_method`、`invalid_params`、
`not_found`、`conflict`、`busy`、`limit_exceeded`、`worker_not_running`、`worker_exited`、
`timeout`、`outcome_unknown`、`pi_error`、`resync_required`、`internal`，
以及接入层的 `unauthorized`、`host_denied`、`origin_denied`。
错误信息不包含 token、模型 key 或会话正文。

## 命令

`GET /api/v1/capabilities` 返回权威清单，测试断言其中每个方法都不会返回
`unsupported_method`。当前分组：

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
| 文件与 Git | `files.list`、`files.stat`、`files.read`、`files.roots`、`git.status`、`git.diff` |
| 其他 | `session.stats`、`session.set_name`、`session.last_assistant`、`session.commands`、`session.export_html`、`sessions.search`、`sessions.delete` |
| 模型配置 | `config.models`、`config.models.raw`、`config.models.write`、`config.models.discover`、`config.models.test`、`config.catalog` |
| 资源清单 | `config.packages`、`config.settings`、`config.trust` |

共同约束：

- `session.start` 的 cwd 必须在启动配置允许的根内；客户端不能传可执行文件、额外 CLI 参数或任意环境变量
- 恢复会话时 cwd 必须与会话头一致，不允许借 cwd 改写原会话所属项目
- `session.switch` 只接受受管会话目录内的文件
- `session.set_thinking` 必须落在 `session.thinking_levels` 返回的列表内
- `session.export_html` 只接受桥构造的导出目录加受限文件名
- `files.*`、`git.*`、`terminal.*` 全部限制在授权工作区内
- `config.settings`/`config.trust`/`config.packages` 只读，密钥字段递归打码
- `config.models.write` 原子写入，落盘前做结构校验；密钥字段原样保留
- `config.models.discover`/`test`/`catalog` 访问外部地址，URL 只允许 http(s)，
  自定义头部拒绝控制字符，供应商错误码透出不吞掉
- 远程安装/更新 Pi 包刻意不实现

## 事件

```json
{"version":1,"kind":"event","sessionId":"...","streamId":"worker","epoch":"worker-generation","seq":42,"event":"pi.event","data":{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}}
```

epoch 每次 worker 启动改变；seq 在该 worker 内递增。A 阶段保留 Pi 原始事件作为 pi.event 载荷，并明确依赖基线版本，不假装已完成跨 agent 的统一消息投影。worker 的退出/状态变化使用 bridge.worker_state。

Pi message_update 不带累计 message，浏览器按 contentIndex 组装增量；message_end.message 是完整权威值。不要仅凭 agent_end 宣告 settled。agent_settled、直接 bash 的终态、扩展交互/重试状态共同决定是否可回收。

订阅前先注册有界接收队列，再返回订阅确认，之后发送事件。网络端写入由单一 writer 串行化，多个命令可在后台等待各自 Pi 回复，避免 abort 被阻塞。每个客户端有独立容量上限；超限关闭慢订阅/连接，Pi 继续执行。

## 重连、去重与不确定结果

同 epoch 且游标仍在补发环内时补发；epoch 不匹配或序号已被淘汰时返回
`resync_required`。补发环按 epoch 隔离，条数与字节双上限，身份变更后序号归零。
新订阅返回当前 epoch/seq；无法恢复中途丢失的 delta 时客户端重新读持久历史，
等待后续权威 `message_end`。

`requestId`、`seq`、`entryId` 是三个维度：前者用于跨重启去重，第二个是传输游标，
最后一个是持久历史游标，不可混用。

跨重启去重语义：

- 同一 `requestId` 已执行过（回执为 `ok`/`error`/`unknown`）时直接回放结论，
  响应带 `duplicate: true`，绝不重新执行
- 回执为 `rejected`（协议层拒绝，命令从未送达 Pi）时允许客户端重试
- 连接内 `seen` 表作为回执不可用时的第二道防线

结果处理：

- 客户端不自动重发 prompt、bash 等可变更命令
- 命令已写入 stdin 但超时或进程退出时可能已生效，返回 `outcome_unknown`
- ws 断开不取消已接受的请求；桥关闭与 worker 停止有自己的终止边界
- 缺少中途快照时明确标记缺口，不伪造补齐
- 有待回复扩展对话时 worker 判定为忙，拒绝非强制停止；强制停止前先取消全部对话

## 历史

HTTP 响应：

```json
{"sessionId":"...","leafId":"last-durable-entry","leafSource":"disk","entries":[],"oldestEntryId":null,"hasMore":false}
```

- 页大小按原始树条目计数，非 UI 可见消息数量。
- entries 始终按祖先到后代排序。before 排除自身，并且必须属于选定 leaf 的祖先链。
- 缺省 leafId 取磁盘可恢复叶子，不声称等于 live Pi 当前内存中的导航位置。
- 会话 ID 由受管根内文件头解析，不能把 URL ID 当绝对路径。
- 初版仅支持 v3。文件、行、索引条目和输出字节都有上限；超限显式返回错误，不用 0/空数组假装无历史。
- 完整损坏记录、重复 ID、缺父节点、循环均显式报错；正在追加的末尾半行忽略。
- 历史读取不修改文件，不触发 Pi 自动迁移、不启动 worker
- 搜索同样只读，命中数、文件数、单文件体积、单行字节都有上限，达到即停并标记 `truncated`
- 删除优先使用 `trash`；删除后索引立即失效

后续 HTML fragment 接口渲染同一 page projection，并提供稳定 entryId/groupId 和分页占位；DOM 不因补页重新折叠已有内容。

## 运行限额与可观测性

限额集中在配置，不散落在业务逻辑中：

| 类别 | 上限 |
|---|---|
| 进程 | worker 数、终端数、启动超时、请求超时、关闭宽限、空闲超时 |
| 帧 | stdout 单行字节、HTTP/WS 帧字节、连接发送队列字节 |
| 订阅 | 每 worker 订阅数、每订阅消息数与字节、补发环条数与字节 |
| 会话 | 历史文件字节、单页字节、条目数、会话文件数、搜索命中数 |
| 去重 | 每连接 requestId 数、回执内存条数、回执文件字节与轮转数 |
| 指标 | 方法名 64 个、错误码截断到 32 字符 |
| 隧道 | 虚拟连接数、空闲时间、发送队列字节 |

`/healthz` 只返回健康状态，不含路径与会话信息；`/api/v1/metrics` 需鉴权，
输出指标、索引、回执、worker、终端与隧道状态，不含正文与密钥。
日志只记录 requestId、sessionId、方法、状态、耗时与错误类别。
指标按桥、worker 与子进程分别统计，不以进程地址空间 VSZ 代替实际内存占用。

## 云端隧道

浏览器经 relay 连接本地桥时，帧外面包一层最小路由封装：

```json
{"to":"<目标 clientId>","from":"<来源 clientId>","data":<原始业务帧>}
```

- relay 只读 `to`/`from` 做转发，绝不解析 `data`
- 桥到 relay 必须带 `to`，缺失即丢弃，不做广播
- relay 到桥带 `from`，桥据此把回复发回正确标签页
- 同一 `clientId` 重连会顶掉旧连接；多标签需各自使用不同 clientId
- relay 不落盘会话正文、不接触模型密钥；设备与用户令牌只存 SHA-256
