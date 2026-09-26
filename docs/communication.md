# 前端 ↔ 桥 通信设计

本文固定 `pi-webui-htmx` 与 `pi-bridge-go` 之间的通信方式。
契约的强制部分在 [`api/v1/protocol.md`](../api/v1/protocol.md)，
本文解释**为什么这样设计**，以及哪些是 Pi 的硬约束推出来的结论。

---

## 1. 三层，不是一层

```
浏览器 ──1. HTTP/HTML──▶ 桥 ──▶ UI 包模板渲染
       ──2. WS/JSON────▶ 桥 ──▶ 命令与事件
       ──3. stdio/JSONL─▶ pi ──▶ 真正的 agent
```

| 层 | 协议 | 内容 | 为什么 |
|---|---|---|---|
| 1 | HTTP + htmx | HTML 片段 | 请求-响应类界面：侧栏、历史、设置、文件、模型、包清单 |
| 2 | WebSocket + JSON | 命令、事件、终端 IO | 双向、有序、需要回执 |
| 3 | stdio JSONL | Pi RPC | Pi 自己的协议，桥不过度包装 |

**不要用 htmx 做流式对话。** Pi Web 的教训是逐 token 重渲染整条消息会丢光标、
闪烁。第 1 层只负责「一整块已经确定的 HTML」。

**不要用 SSE 替代 WS。** prompt/steer/follow-up/abort、扩展对话回执、
终端输入都是双向的。SSE 得再配一条 POST，跨源时两条连接各自鉴权，
且无法保证同 session 内的顺序。

---

## 2. Pi 的三个硬约束（决定了大部分设计）

### 2.1 命令响应 ≠ 任务完成

`docs/rpc.md` 原话：*The command response is emitted after the prompt is
accepted, queued, or handled. Failures after acceptance are reported through
the normal event and message stream, not as a second `response`.*

所以：

- UI 收到 `ok:true` 只能理解为「已受理」
- 真正结果从事件流的 `agent_settled` 来
- 受理后失败**不会**有第二条 response

### 2.2 只有 delta，没有快照

`message_update` 明确写了 *Contains a delta event without a cumulative
message snapshot*。完整内容只出现在两处：

- `text_end.content` —— 单个内容块结束时
- `message_end.message` —— 整条消息结束时

**推论：断线重连不能靠补发 delta 恢复正在生成的文本。**
补发环（replay）只对「还没丢的序号」有效；一旦序号被淘汰，
客户端必须重新读持久历史，然后等下一个 `message_end`。
桥不得伪造「已恢复」——缺口要显式告知。

### 2.3 扩展 UI 是两类，不是一类

`createExtensionUIContext` 里：

- `select`/`confirm`/`input`/`editor` 阻塞等回执
- `setStatus`/`setWidget`/`notify`/`setTitle`/`set_editor_text` 是 fire-and-forget
- `setFooter`/`setHeader`/`custom`/`onTerminalInput` 等 12 个方法**不往 RPC 转**

推论：桥必须按方法分类。把 fire-and-forget 也登记成待回复对话，
会让 worker 永久停在 `waiting_input` 并挡住空闲回收
（这个 bug 真实存在过，见 `internal/runtime/manager.go` 的 `needsDialogResponse`）。

---

## 3. 命令面

### 3.1 单一 WebSocket

一个连接同时承载命令与事件。握手：

```json
{"version":1,"kind":"command","requestId":"ui-1","sessionId":"...","method":"session.subscribe"}
```

订阅后该 session 的事件推到同一连接。多 session 用多条 subscribe。

### 3.2 requestId 三重作用

| 作用 | 范围 | 实现 |
|---|---|---|
| 响应关联 | 单次调用 | `pending[id]` |
| 连接内防重 | 连接生命周期 | `seen` 表，有上限 |
| 跨重启去重 | 桥重启后仍有效 | 有界追加日志（回执） |

跨重启语义：

- 回执 `ok`/`error`/`unknown` → 直接回放结论，**绝不重新执行**
- 回执 `rejected`（协议层拒绝，命令从未送达 Pi）→ 允许重试
- 无回执 → 正常执行

**`unknown` 是个真实状态**，不是错误。命令已写入 stdin 但超时或进程退出，
可能已生效。此时返回 `outcome_unknown`，客户端必须自己去对账，
不能当作失败重试。

### 3.3 取消不被堵住

`internal/pi/client.go` 把控制帧和命令帧分成两个队列：

```
notifies (256) ──优先──┐
                       ├─▶ 单一写协程 ──▶ stdin
writes   (64)  ────────┘
```

控制帧（扩展回执、取消对话）优先于命令帧。否则一个卡住的长命令
会让 `abort` 排在后面，Pi 继续跑。

控制帧队列**不得**在满时杀死连接——那会让整个会话一起死。
满时返回 `ErrLimit`，调用方知道没送达。

---

## 4. 背压与限额

原则：**慢消费者绝不影响 Pi，也绝不被无限缓冲拖垮桥。**

| 位置 | 限额 | 超出后的行为 |
|---|---|---|
| Pi stdout 读取 | 持续读取，不暂停 | — |
| 单事件帧 | 256 KiB | 发 `bridge.event_omitted` + `resyncRequired` |
| 每订阅者 | 32 条 / 1 MiB | 摘除该订阅者并关闭 |
| 补发环 | 256 条 / 1 MiB，按 epoch 隔离 | 老序号返回 `resync_required` |
| WS 单帧 | 请求 1 MiB / 响应 512 KiB | 拒绝 |
| 连接队列 | 1 MiB | 拒绝 |
| 在途命令 | 16 | `limit_exceeded` |
| 回执日志 | 4096 条 / 4 MiB × 3 轮转 | 淘汰最旧 |

事件超限时**不丢弃了事**：发一个明确的 `resyncRequired` 帧，
客户端据此重新读历史。静默丢失比显式缺口更难调试。

---

## 5. 滚动位置

这是 Pi Web 的真实 bug，必须在前端契约里解决。

**Pi Web 的做法**（`lib/chat-lazy-load.ts`）：

```ts
captureScrollDistance(scrollHeight, scrollTop) => scrollHeight - scrollTop
restoreScrollTop(scrollHeight, saved) => Math.max(0, scrollHeight - saved)
```

这个公式只在**新增高度全部位于视口上方**时正确。而 Pi Web 按单条消息
切片分页，刀常落在一轮对话中间，于是：

- 已在屏幕上的 assistant 被重新折进 `ProcessDetailsGroup`
- 新增高度有一部分在当前这一轮**内部**
- 保持「离底部距离」等于把底部钉死，中间被撑开
- 用户正在读的那段向下移

而且修正 effect 只依赖 `visibleCount`，长度没越过 50 时根本不执行。

**我们的做法**：从结构上排除，而不是补公式。

1. **整轮渲染**：一个历史片段只含完整回合，插入位置永远在轮边界
2. **桥下发滚动模式**：响应头 `X-Scroll-Mode: prepend|append`
   - `prepend`：保持离底部距离（新增内容全在上方）
   - `append`：滚到新片段
3. **孤儿 assistant 单独成轮**，不并入上一轮，避免翻页时被重新折叠

模板不写滚动逻辑，全部由桥的响应头决定。

---

## 6. 云端隧道

浏览器经 relay 连本地桥时，帧外面包一层最小路由封装：

```json
{"to":"<目标 clientId>","from":"<来源 clientId>","data":<原始业务帧>}
```

- relay **只读 `to`/`from`**，绝不解析 `data`
- 桥到 relay 必须带 `to`，缺失即丢弃，不做广播
- 同一 `clientId` 重连会顶掉旧连接；多标签各自用不同 clientId
- relay 不落盘会话正文、不接触模型密钥

设备撤销必须**立即**中断活跃隧道与浏览器连接，不能只等自然掉线。

---

## 7. 不做的事

| 不做 | 理由 |
|---|---|
| 二进制协议（protobuf/cbor/msgpack） | JSON 可读、可 `curl` 调试、TS 类型直接对应。流量不是瓶颈 |
| htmx 做流式 | 逐 token 替换 HTML 会丢光标、闪烁 |
| SSE 替代 WS | 双向场景下 SSE 要配 POST，顺序与鉴权都更麻烦 |
| 桥侧累积完整会话 | 会话由 Pi 独占；桥只保留有界的传输状态 |
| 伪造无损重连 | Pi 只有 delta 没有快照，缺口必须显式 |
| WebRTC | 本地直连场景下 WS 已够；P2P 打洞在 NAT 后不稳定 |
| 长轮询降级 | WS 不可用时直接报错，不做半可用状态 |

---

## 8. 变更规则

| 变更 | 是否破坏契约 |
|---|---|
| 增删模板、改样式、改类名 | ❌ |
| 给响应**增加**字段 | ❌ |
| **重命名或删除**字段 | ✅ |
| 改 URL | ✅ |
| 增加对桥命令的依赖 | ✅（要进 `requiredMethods`） |
| 改错误码集合 | ✅ |
| 改 `protocolVersion` | ✅ |

UI 可以随时改样子，不能单方面改协议。
