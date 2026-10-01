# 前端 ↔ 桥 ↔ Pi 通信约定

本页描述三层之间的分层、顺序与失败语义。在线契约（入口、方法、事件形状）见
[protocol.md](../api/v1/protocol.md)；设计决策与边界见 [architecture.md](architecture.md)。

## 1. 三层职责

```text
浏览器 ── HTTP：页面/片段/受控资源 ──▶ Go presentation / store
       ── WS：命令/回执/事件/PTY ────▶ Go transport / runtime
                                            │
                                            └── stdin/stdout JSONL ──▶ pi --mode rpc
```

- htmx 负责完整 HTML 片段；TypeScript 处理双向命令、流式增量与组件生命周期。发送、模型选择、对话回执都走 WS——不是「只有流式用 JS」。
- 选择 WS 是为了复用一条已鉴权的双向连接与统一的背压实现。WebSocket 字节有序不等于并发业务命令有执行顺序：顺序由服务端按方法类别决定。
- 桥不嵌入 Pi SDK，也不另存一份会话正文：普通历史直接读 JSONL。
- **大内容走 HTTP，控制帧保持小**：文件、图片、bash 完整输出、历史树分页、上传与下载都在受鉴权的 HTTP 端点；WS 只传命令、回执、事件与元数据。

## 2. Pi 约束

| 事实（Pi 0.85.1） | 系统含义 |
|---|---|
| prompt response 表示 accepted/queued/handled | 回执不是任务完成；接受后的失败走事件流 |
| message_update 只有 delta | 重放窗口缺失时不能重建中途完整消息 |
| text_end / message_end 有完整内容 | 用权威结果替换局部投影，不能拼接重复文本 |
| tool_execution_update.partialResult 为累计结果 | 不能当 delta 追加 |
| agent_settled 表示 agent 队列/重试最终收敛 | 直接 bash、扩展对话、PTY 仍有各自独立生命周期 |
| dialog 与 fire-and-forget 是两类 | 只有 select/confirm/input/editor 进入 pending |

setStatus 不需要回执。Pi 可能自行结束对话（含超时）；桥必须收敛本地 pending，不能用「曾经收到过 dialog」永久阻止回收。

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

requestId 在认证主体与设备下唯一，重试同 ID 必须同指纹；UI 请求在发起时捕获目标，等待期间切会话不改投。身份变化同时更换 epoch，旧事件不能切回旧 epoch。

## 4. 命令受理与崩溃语义

可靠日志次序只适用于**须持久化的变更**；只读、连接订阅、临时 PTY 输入与安全控制各有自己的策略。逐按键输入不写盘，存储故障也不堵住取消。

```text
认证 → 解码/权限/实际字节校验 → 配额/目标预留
     → 原子 claim(requestId, fingerprint)
     → durable intent + Sync
     → Pi 入队/写入 → Pi response 或 unknown
     → durable terminal receipt → 回客户端
```

- 同请求并发只有一个执行者，其余等待或查询该请求；同 ID 不同参数返回 conflict。
- 持久 intent 没有终态时，恢复为 `outcome_unknown`，**绝不自动重发**。
- 承诺是**有效记录保留范围内至多派发一次**：Pi 与日志不能原子提交，因此不是 exactly-once，也不能保证有 intent 就一定执行过。
- 无回执不证明没执行（记录可能已过保留期或被删除）。客户端不自动重发变更命令；被明确拒绝且尚未派发的请求可以重试。
- 任意变更命令需要可靠记录时，存储不可用就拒绝新变更；读取、对账与安全取消不被连带禁用。
- 连接中断取消的是等待与输出，不取消已受理执行。长命令的期限来自方法声明，UI 等待期限不得短于服务端宣告期限；超时不自动生成一次新的执行。

### 取消的优先级

Pi client 分 `notifies(256)` 与 `writes(64)` 两组队列，单 writer 优先读取 notify。服务端准入与 stdin 写入两层都为安全控制命令保留有界容量。

优先级不能打断已经在写的帧，因此命令帧本身也必须小且有写入期限。Notify 入队不等于 stdin 写成功；dialog 的消费需要可观察的写入结果。

## 5. 原子订阅与恢复

```text
worker 短锁内：验证 epoch/afterSeq → replay 截止 fence → 注册 live queue
锁外同一 writer：subscription confirmation → replay(<=fence) → live(>fence)
```

- 确认携带当前身份代次；UI 只信当前连接与订阅对应的确认，不从任意事件猜 epoch。迟到的确认有归属守卫，不能改写新会话的游标。
- replay 与 live 积压共用容量预算。窗口不足、事件省略、队列溢出、身份变化时发 resync 或要求重连，不静默遗漏。
- UI 在 omitted/resync 时立即重读持久历史；正在生成的部分标记缺口，等 `message_end` 收敛。
- 退订销毁底层订阅任务，Close 销毁全部 attachment。发送失败的虚拟连接必须注销；同 clientId 重连使用新对象，旧 Close 不得删掉新对象。

## 6. 大小、时间与背压

| 边界 | 当前值 |
|---|---:|
| bridge↔Pi JSONL 单帧 | 8 MiB（超限关闭 RPC client） |
| 浏览器 WS 读方向单帧 | ≈97 MiB（8 张 × 12 MiB base64 附件 + 1 MiB 封套） |
| 桥→浏览器响应载荷 | 448 KiB（文本超出截断并标记，图片明确拒绝） |
| 连接数与在途操作 | 8 连接 / 16 在途 |
| 出站待发队列 | WS 32 帧；tunnel 64 帧 / 2 MiB；replay 整批最多等 5 秒 |
| 单事件 | 256 KiB |
| 订阅队列 / 重放环 | 32 条 / 1 MiB；256 条 / 1 MiB |
| 回执内存/日志 | 4096 条；每文件 4 MiB，最多 3 个轮转文件 |
| 运行时操作等待 | 30 秒；长任务（如 compact）按方法声明加长 |

规则：

1. capabilities 与执行层引用同一份限额来源，客户端按声明判断能不能发；用户传入的 limit 只能降低服务端上限。
2. 数据在入队/分配/启动 goroutine **之前**检查或预留预算；实际读取用 reader 上限，不只 Stat 预检。
3. 受控超限返回 `limit_exceeded`/`truncated` 并尽量保住连接；只有非法 framing 才关闭故障连接。Pi stdout 持续消费，不向 Pi 传播浏览器背压。
4. JSON、base64、路由转义之后的完整长度才是线上字节数；route 信封开销单独计入隧道帧预算。
5. 控制操作有保留容量；慢客户端的数据下载可被取消，但不挤占 abort。

## 7. 页面读取、流式与滚动

每次页面读取持有会话代次、面板序号与目标节点；切换时取消旧读请求。htmx 在 `beforeOnLoad`（响应头处理之前）拒绝过期响应，`beforeSwap` 再核对目标——在 Promise 返回后再检查已经太晚。

滚动由前端根据用户位置决定：

- 切换前已贴底才跟随新内容；正在读历史时保留稳定 `entryId` + 视口偏移。
- prepend、append、replace 各有策略；`X-Scroll-Mode` 只是诊断提示，服务端看不到用户是否贴底。
- 分页优先在回合边界，长回合仍受字节/条目硬限额。
- Markdown/图片/公式异步增高后继续锚点校正，不无条件拉到底部。

## 8. 云端链路

浏览器始终使用同源 HTTP 与 WS，只有受信 basePath 不同：

```text
浏览器 /d/{deviceId}/... → relay 认证 + 设备路由
                        → 有界 tunnel 帧 → 本地相同 HTTP handler / Executor
```

- relay 只做路由：读 `id` 配对与 `to`/`from`，不解析业务载荷。
- HTTP 转发：请求封装成隧道帧的 `http` 字段（方法/路径/头/体），设备侧在**桥自己的 HTTP handler** 上执行同一请求，响应按 ID 配对回传。`/d/{id}` 308 规范化为 `/d/{id}/`，保证相对 URL 始终落在前缀内。
- 桥侧信任模型与 WS 路径相同，但**不改动自身的 Host/Origin 校验**：Host 由桥设定，Origin 由 relay 剥离。
- 上限：请求体 4 MiB、响应按桥的内容上限派生；超限回 413/502。分片与云端分块上传未实现——四层上限自洽，没有装不下的内容。
- 设备 token 放 upgrade Authorization；非环回只允许 WSS。TLS 中继可见转发明文：**不落盘不等于端到端加密**。

## 9. 变更纪律

- 新增可选字段/方法可以通过能力协商；当前协议版本为 **v1**，订阅确认、请求状态、配置 revision 与秘密操作都在 v1 的形状内。
- 发布后不保留旧的盲写/去重回退路径。
- 不靠「无期限猜形状」掩盖协议漂移；范围选择、默认值、错误码以 [protocol.md](../api/v1/protocol.md) 与代码为准，本页只解释次序，不复制方法清单。
- 每个修复都要有与源码行为相符的判定性测试；模板正则不替代端到端接线。
