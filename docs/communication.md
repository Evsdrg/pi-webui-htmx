# 前端 ↔ 桥 ↔ Pi 通信约定

更新：2026-09-27。本文描述分层、顺序和失败语义。[protocol.md](../api/v1/protocol.md) 记录 v1 接口现状；[architecture.md](architecture.md) 的 S01–S12 是待实现修复设计，[repair-plan.md](repair-plan.md) 统一实施依赖、影响与迁移。**标为目标的流程尚未生效，不能用于声称当前实现已经安全恢复。**

## 1. 三层职责

```text
浏览器 ── HTTP：页面/片段/受控资源 ──▶ Go presentation / store
       ── WS：命令/回执/事件/PTY ────▶ Go transport / runtime
                                            │
                                            └── stdin/stdout JSONL ──▶ pi --mode rpc
```

- htmx 负责完整 HTML 片段；TypeScript 处理双向命令、流式增量和组件生命周期。不是“只有流式可以用 JS”，发送、模型选择、对话回执等当前都走 WS。
- 选择 WS 是为了复用双向连接、鉴权和背压实现；不是宣称 HTTP+SSE 无法实现这些语义。WebSocket 字节有序也不等于并发业务命令有执行顺序。
- bridge 不嵌入 Pi SDK。普通历史直接读 JSONL；树/导出只读解耦尚未完成。
- **目标 S06：** 大文件、图片、bash 完整输出、上传、导出走 HTTP 数据通道，WS 只传控制和小元数据。当前 `files.read/files.image` 仍可把大内容放进 WS，存在限额错配。

## 2. Pi 约束

| 事实（Pi 0.85.1） | 系统含义 |
|---|---|
| prompt response 表示 accepted/queued/handled | 回执不是任务完成；接受后的失败走事件流 |
| message_update 只有 delta | 重放窗口缺失时不能重建中途完整消息 |
| text_end / message_end 有完整内容 | 使用权威结果替换局部投影，不能拼接重复文本 |
| tool_execution_update.partialResult 为累计结果 | 不能当 delta 追加 |
| agent_settled 表示 agent 队列/重试最终收敛 | 直接 bash、扩展对话、PTY 仍有独立生命周期 |
| dialog 与 fire-and-forget 是两类 | 只有 select/confirm/input/editor 进入 pending |

setStatus 不需要回执。Pi timeout 可能自行结束对话；桥必须收敛本地 pending，不能用“曾经收到过 dialog”永久阻止回收。

## 3. 标识与作用域

| 标识 | 含义 | 不能替代 |
|---|---|---|
| requestId | 一次用户命令的关联/去重键 | sessionId、entryId |
| 内部 RPC id | bridge↔Pi 调用关联 | 外部 requestId、扩展 dialog id |
| workerID | 一个进程实例 | 会随 fork/switch 改变的 sessionId |
| epoch + seq | 一个 worker 身份代次中的传输游标 | JSONL 持久 entryId |
| entryId + blockIndex | 持久记录及其内容块 | 回合末尾 assistant ID |
| UI generation / request sequence | 页面作用域与面板读取的新旧关系 | 命令在服务端的取消证明 |
| connection generation | 同 clientId 重连的新连接 | 用户/设备授权身份 |

目标：requestId 在认证主体/设备下唯一，重试同 ID 必须同指纹；UI 请求发起时捕获目标，在等待后不得重新读取全局 sessionId。身份变化同时更换 epoch，旧事件不能切回旧 epoch。

## 4. 命令受理与崩溃语义（目标 S01）

以下可靠日志次序只适用于须持久化的变更；只读、连接订阅、临时 PTY 输入及安全控制使用整体规划中的各自策略。不能把逐按键输入同步写盘，也不能让存储故障堵住取消。

```text
认证 → 解码/权限/实际字节校验 → 配额/目标预留
     → 原子 claim(requestId, fingerprint)
     → durable intent + Sync
     → Pi 入队/写入 → Pi response 或 unknown
     → durable terminal receipt → 回客户端
```

- 当前实现只在命令完成后记录回执，仍有 B04/B30/B31/B47/B58 的窗口；不能声称已经完成跨重启 at-most-once。
- 同请求并发只有一个执行者；其余等待/查询该请求。同 ID 不同参数 conflict。持久 intent 没终态，恢复为 outcome_unknown，绝不自动重发。
- 这是**有效记录保留范围内至多派发一次**；Pi 与日志不能原子提交，不能承诺 exactly-once，也不能保证有 intent 就一定执行过。
- 无回执不证明没执行：记录可能已过保留期或被运维删除。客户端始终不自动重发变更命令。被明确拒绝且尚未派发的请求可以按协议重试。
- 任意变更命令需要可靠记录时，存储不可用就拒绝；读取/对账/安全取消不应被连带禁用。
- 连接中断取消的是等待与输出，不取消已受理执行。长命令期限来自同一方法策略；UI 等待期限不得短于服务端宣告期限。超时不会自动生成一次新的执行。

### 取消的优先级

当前 Pi client 已分 `notifies(256)` 与 `writes(64)`，单 writer 优先读取 notify。**这不代表所有 abort RPC 已进入优先队列**。目标是服务端准入和 stdin 写入两层都给安全控制命令保留有界容量，并按方法分类接线。

优先级不能打断已经在写的帧；所以命令帧也必须小且有写入期限。Notify 入队也不是 stdin 写成功；dialog 消费需要可观察的写入结果。

## 5. 原子订阅与恢复（目标 S02）

```text
worker 短锁内：验证 epoch/afterSeq → replay 截止 fence → 注册 live queue
锁外同一 writer：subscription confirmation → replay(<=fence) → live(>fence)
```

- 确认携带当前身份代次；UI 只信当前连接/订阅对应确认，不从任意事件猜 epoch。当前 Workbench.subscribe 未消费确认的身份数据，需在客户端建立确认状态后才派发该订阅的业务事件；只改服务器发送顺序不能替代这个调用方修改。
- replay + live 积压共用容量预算。窗口不足、事件省略、队列溢出、身份变化必须发 resync 或关闭需重连的订阅，不能静默遗漏。连接出站队列瞬时满时按独立预算短暂等待；worker 的订阅配额依旧独立，高速流仍可能把慢订阅者摘除。
- UI 在 omitted/resync 时立即重读持久历史，清除“不完整 live 已恢复”的假象；正在生成的部分标记缺口，等 message_end。
- 退订销毁底层订阅任务，Close 销毁全部 attachment。发送失败的虚拟连接必须注销；同 clientId 重连使用新对象，旧 Close 不得删新对象。

## 6. 大小、时间与背压

当前代码默认值（不是所有路径都已统一执行）：

| 边界 | 当前默认 | 已知差距 |
|---|---:|---|
| bridge↔Pi JSONL 单帧 | 8 MiB | 超限会关闭 bridge 的 RPC client；大树失败不能直接归咎 Pi |
| 浏览器 WS 请求/响应 | 1 MiB / 512 KiB | base64 图片、文件和完整输出可超过可传输上限 |
| 连接出站待发队列 | WS 32 帧、tunnel 64 帧；总计 1 MiB；replay 整批最多等 5 秒 | 缓冲暂满时等待消费者，不因此直接关闭连接；慢消费者超时需重新同步 |
| 单事件 | 256 KiB | 过大 dialog 尚有永久等待风险 |
| 订阅队列 | 32 条 / 1 MiB | tunnel 退订/并发访问尚有缺陷 |
| 重放环 | 256 条 / 1 MiB | 注册与重放快照尚未原子化 |
| 回执内存/日志 | 4096 条 / 每文件 4 MiB / 最多 3 个轮转文件 | 不构成未经修复的可靠去重保证 |
| runtime 操作等待 | 30 秒 | UI compact 120 秒不能改变服务端期限 |

目标规则：

1. 同一本限额配置生成 capabilities，UI 读实际值；用户 limit 只能降低服务端上限。条数、字节、并发、时间分开限制。
2. 所有数据在入队/分配/启动 goroutine **之前**检查或预留预算；实际 reader 施加 N+1 字节上限，不仅 Stat 检查。
3. 受控超限返回 limit_exceeded/truncated 并尽可能保住连接；非法 framing 才关闭对应故障连接。Pi stdout 持续消费，不向 Pi 传播浏览器背压。
4. JSON/base64/路由转义后的完整长度才是线上的字节。上传上限不能独立于 Pi 的 8 MiB；推荐的附件预算见 S06，未实施前仍是旧限制。
5. 未完成控制操作有保留容量，数据下载/输出慢客户端可被取消，不挤占 abort。

## 7. 页面读取、流式与滚动

目标 S07：每次页面读取持有 session generation、面板序号和目标节点；切换时取消旧读请求。htmx `beforeOnLoad` 在响应头处理前拒绝过期响应，`beforeSwap` 再检查目标；Promise 返回后检查已经来不及。

滚动由前端根据用户位置决定：

- 切换前已贴底才跟随新内容；正在读历史时保留稳定 entryId+视口偏移。
- prepend、append、replace 各有策略，`X-Scroll-Mode` 只是诊断/提示，服务端看不到用户是否贴底。
- 分页优先在回合边界；长回合仍受字节/条目硬限额，不能为了“整轮”无限读取。后续分段需要稳定 group/segment ID，避免重组已显示 DOM。
- Markdown/图片/公式异步增高后继续锚点校正，UI 不由响应头无条件拉到底部。

## 8. 云端链路（目标 S09/S12，当前未接通）

目标是浏览器仍使用同源 HTTP 与 WS：

```text
浏览器 /devices/<id>/... → 云 relay 授权/设备路由
                        → 有界 tunnel → 本地相同 HTTP/Executor
```

- 设备前缀是待实现路由示意，不是当前可用 URL；同标签页固定设备，切换设备重建 UI scope。
- 现有 tunnel 的 `from/data` 与 `to/data` 信封只解决 WS 路由，尚不足以转发 HTMX 片段、资产、上传和下载。
- 新 HTTP tunnel 必须使用允许的资源类型/路径、request ID、分块、credit/取消和预算，不能提供任意 URL 代理；route 开销单独计入线上帧限制。
- relay 认证后填可信主体/设备路由，本地桥验证隧道身份及授权；浏览器不能伪造 from 或借另一设备读取资源。
- 设备 token 在 upgrade Authorization；非环回 WSS；明确 public origin 和受信代理来源。TLS 中继可见内容，不落盘不等于 E2EE。
- 本地与云端使用同一版本的模板/资源/协议。HTTP+WS+图片+导出+上传闭环通过前，不能把后端 tunnel 已有等同云产品已可用。

## 9. 变更纪律

新增可选字段/方法可以能力协商；完整修复涉及订阅、请求结果、秘密与资源语义，整体规划已选择在 P6 成对升级为 v2。此前仍保留当前 v1 的明确形状，不提前改版本；发布后不保留旧盲写/去重路径作回退。不得继续靠无期限猜形状掩盖协议漂移。

范围选择、默认值、错误码由对应契约维护；本文件解释次序，不复制另一份方法清单。迁移与回滚按整体规划执行，每个修复必须有与源码行为相符的判定性测试，模板正则不替代端到端接线。
