# Bridge Protocol v1

状态：本地 A 阶段契约及后续兼容规则。实现能力以 `/api/v1/capabilities` 为准；文档中明确标记为后续的能力不得在握手中声明可用。

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

错误码至少包括 invalid_request、unsupported_version、unsupported_method、invalid_params、not_found、conflict、busy、limit_exceeded、worker_not_running、worker_exited、timeout、outcome_unknown、pi_error、internal。错误不包含 token 或模型 key。

## A 阶段命令

| method | params | 行为 |
|---|---|---|
| session.start | cwd；可选 sessionId（放 envelope） | 无 id 时创建 Pi；有 id 时恢复允许目录内的会话；返回状态/epoch |
| session.state | 无 | 查询已启动 worker，不隐式启动 |
| session.prompt | text；可选 streamingBehavior=steer/followUp | 映射 Pi prompt；accepted 不是任务完成 |
| session.abort | 无 | 先 clear_queue，再 abort，避免队列在 abort 后继续 |
| session.stop | 可选 force=false | 空闲可停；忙碌需显式 force |
| session.subscribe | 可选 epoch/afterSeq | 订阅事件；是否支持 replay 由 capabilities 声明 |
| session.unsubscribe | 无 | 取消该连接的订阅，不取消任务 |
| worker.list | 无 | 当前受管 worker 状态，不扫描模型 |

启动命令的 cwd 必须在启动配置允许的根中；客户端不能传可执行文件、额外 CLI 参数或任意环境变量。CLI executable、agent-dir、session-dir 均为本机配置。恢复时 cwd 取会话头并重新验证；不允许借 cwd 改写原会话所属项目。

A 阶段不开放 switch_session/new_session/fork 等会改变 worker 会话身份的原始命令，后续由 runtime 在事务性身份更新后暴露。模型设置、扩展 dialog 回包、PTY 和文件操作后续添加，不能先声称已实现。

## 事件

```json
{"version":1,"kind":"event","sessionId":"...","streamId":"worker","epoch":"worker-generation","seq":42,"event":"pi.event","data":{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}}
```

epoch 每次 worker 启动改变；seq 在该 worker 内递增。A 阶段保留 Pi 原始事件作为 pi.event 载荷，并明确依赖基线版本，不假装已完成跨 agent 的统一消息投影。worker 的退出/状态变化使用 bridge.worker_state。

Pi message_update 不带累计 message，浏览器按 contentIndex 组装增量；message_end.message 是完整权威值。不要仅凭 agent_end 宣告 settled。agent_settled、直接 bash 的终态、扩展交互/重试状态共同决定是否可回收。

订阅前先注册有界接收队列，再返回订阅确认，之后发送事件。网络端写入由单一 writer 串行化，多个命令可在后台等待各自 Pi 回复，避免 abort 被阻塞。每个客户端有独立容量上限；超限关闭慢订阅/连接，Pi 继续执行。

## 重连、去重与不确定结果

目标：同 epoch 且游标仍在 replay 范围内时补发，否则返回 resync_required。A 阶段可以声明 replay=false；新订阅返回当前 epoch/seq，并明确无法恢复中途丢失的 delta，客户端重新读持久历史，等待后续权威 message_end。

requestId、seq、entryId 是三个不同维度。A 阶段仅做连接内 requestId 防重（有上限），不承诺跨连接/跨重启 exactly-once。持久命令回执与重放属于 B 阶段。

- 客户端不自动重发 prompt、bash、文件写入等可变更命令。
- 命令已写入 stdin，但超时/进程退出时可能已经生效，返回 outcome_unknown；不能返回未执行。
- ws 断开不取消已经接受的请求。操作 ctx 不绑定浏览器连接 ctx；桥关闭/worker 停止有自己的终止边界。
- 缺少中途快照时明确标记缺口，不伪造补齐。

## 历史

HTTP 响应：

```json
{"sessionId":"...","leafId":"last-durable-entry","leafSource":"disk","entries":[],"oldestEntryId":null,"hasMore":false}
```

- A 阶段页大小按原始树条目计数，非 UI 可见消息数量。
- entries 始终按祖先到后代排序。before 排除自身，并且必须属于选定 leaf 的祖先链。
- 缺省 leafId 取磁盘可恢复叶子，不声称等于 live Pi 当前内存中的导航位置。
- 会话 ID 由受管根内文件头解析，不能把 URL ID 当绝对路径。
- 初版仅支持 v3。文件、行、索引条目和输出字节都有上限；超限显式返回错误，不用 0/空数组假装无历史。
- 完整损坏记录、重复 ID、缺父节点、循环均显式报错；正在追加的末尾半行忽略。
- 历史读取不修改文件，不触发 Pi 自动迁移，不启动 worker。

后续 HTML fragment 接口渲染同一 page projection，并提供稳定 entryId/groupId 和分页占位；DOM 不因补页重新折叠已有内容。

## 首版运行限额与可观测性

实现时限额集中在配置，不散落在业务逻辑中：worker 数、启动超时、请求超时、关闭宽限、空闲超时、stdout 单行字节、HTTP/WS 帧字节、订阅容量、单连接命令数、历史文件/页大小。

/healthz 不含正文；日志仅记录 requestId、sessionId、方法、状态、耗时和错误类别。调试日志也不记录 Authorization 或完整模型配置。指标分别统计桥、worker 与子进程，不以进程地址空间 VSZ 代替实际内存占用。
