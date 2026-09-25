# Pi Bridge（Go）

一个用 Go 写的本地桥：管理独立的 `pi --mode rpc` 子进程，向浏览器提供受控的 HTTP/WebSocket 接口。
目标是把 agent 运行时从网页服务器里拆出去，让内存随进程退出真正归还。

**当前进度：A 阶段（本地闭环）。** 只实现了下面列出的能力；未列出的都在后续阶段，未实现的功能一律明确报错，不会假装成功。

## 已实现

| 能力 | 说明 |
|---|---|
| 显式启动/恢复会话 | 只有客户端明确要求时才拉起 Pi；列表与历史查询不会启动 |
| 发送提示词 | 只返回「已接受」，不伪造任务完成 |
| 中断 | 先 `clear_queue` 再 `abort`，避免 abort 后队列继续执行 |
| 停止工作进程 | 默认拒绝忙会话，`force` 必须显式；分级停止 stdin → SIGTERM → SIGKILL |
| 事件订阅 | 有界队列，慢订阅者被摘除并关闭，绝不阻塞 Pi 输出 |
| 空闲回收 | 空闲超时后退出进程，内存随之归还 |
| 会话列表与历史 | 只读磁盘 v3 JSONL，按分支分页，不启动 Pi、不改写文件 |
| 鉴权 | Bearer token 或 HMAC Cookie；Host 与 Origin 独立校验 |
| 限额 | 连接数、在途命令数、帧大小、队列字节、单页体积 |

## 未实现（后续阶段）

模型与思考强度切换、压缩与重试控制、fork/clone/切换会话、树导航、工具动态配置、bash、终端 PTY、文件与 Git、模型与凭据管理、插件技能安装、事件补发（replay）、跨重启去重、云端 relay 与隧道、扩展对话界面（当前直接取消）。

## 运行

```bash
export PI_BRIDGE_TOKEN="至少 32 个随机字符"
go run ./cmd/pi-bridge \
  --workspace /srv/projects/pi \
  --listen 127.0.0.1:30142 \
  --pi "$(command -v pi)" \
  --state-dir /tmp/pi-bridge-state \
  --idle-timeout 2m \
  --max-workers 4
```

参数说明：

- `--workspace`：必填，唯一允许的工作区根；Pi 只能在这个目录下启动
- `--listen`：**只接受环回 IP**，A 阶段不提供公网监听
- `--pi`：Pi 可执行文件路径，来自本机配置，不接受网络请求指定
- `--state-dir`：桥自有的会话与配置目录，缺省在用户缓存目录下
- `--agent-dir`：Pi 配置目录，缺省使用隔离的 `state-dir/agent`，避免污染真实 `~/.pi/agent`
- `--extensions`：默认关闭。开启后加载 Pi 已配置资源，但**项目信任仍保持拒绝**
- `--idle-timeout` / `--max-workers`：空闲回收时间与并发上限

## 接口

| 端点 | 方法 | 说明 |
|---|---|---|
| `/healthz` | GET | 仅健康状态，不含路径与会话信息 |
| `/api/v1/auth` | POST | Bearer 换取 HttpOnly Cookie |
| `/api/v1/capabilities` | GET | 版本、方法清单、缺口与限额 |
| `/api/v1/sessions?limit&offset` | GET | 会话目录分页 |
| `/api/v1/sessions/{id}/history?limit&leafId&before` | GET | 所选分支历史分页 |
| `/api/v1/ws` | WS | 命令、响应与事件 |

WS 命令：`worker.list`、`session.start`、`session.state`、`session.prompt`、`session.abort`、`session.stop`、`session.subscribe`、`session.unsubscribe`。

协议细节见 [`api/v1/protocol.md`](../api/v1/protocol.md)，模块边界见 [`docs/architecture.md`](docs/architecture.md)，Pi 兼容矩阵见 [`docs/pi-compatibility.md`](docs/pi-compatibility.md)。

## 设计约束

1. **桥不导入 Pi SDK。** 只能通过 stdin/stdout 对话，从语言层面避免把 agent 堆回网页进程。
2. **请求寿命长于浏览器连接。** 断开只停止等待，不取消已接受的任务。
3. **`requestId`、`seq`、`entryId` 是三个维度。** 前者用于连接内防重，后者是传输游标，最后一个是持久历史游标，不可混用。
4. **有副作用命令结果不明时返回 `outcome_unknown`**，客户端不得自动重试。
5. **stdin 写入串行化，但不互等完成**，否则长命令会挡住取消。
6. **stdout 必须持续消费**，慢客户端只影响自己。
7. **历史读取只做只读扫描**，不会触发 Pi 的格式迁移或自动改写。

## 实测（本机，2026-09-26）

隔离配置、未加载扩展、未发送提示词：

| 进程 | RSS | 线程 |
|---|---:|---:|
| `pi-bridge` 桥自身 | **10.2 MiB** | 9 |
| `pi --mode rpc` 工作进程 | **145 MiB** | 43 |
| 合计 | **约 155 MiB** | — |

对照同期 Pi Web 的 `next-server` 约 984 MiB。

已验证：

- 真实 Pi 握手成功，`get_state` 返回真实 `sessionId`
- `session.stop` 后工作进程退出，进程表同步清空
- 对桥发 `SIGKILL` 后**无残留 Pi 进程**（`Pdeathsig` + 独立进程组生效）
- 未发送提示词时隔离会话目录不产生 `.jsonl`，无副作用
- 列表与历史查询全程 0 个工作进程

注意：Pi 的 shim 会 exec，因此 `ps` 里命令名是 `pi` 而不是 `node .../pi`，
用 `ps -C node` 抓不到它。

## 测试

```bash
go vet ./...
go test -race ./...
```

- 单元测试使用 `testdata/fake-pi` 这个可控的假 Pi，**不调用真实模型、不产生费用**
- 真实 Pi 只做隔离配置下的握手与退出验证，见下方「真实 Pi 冒烟」
- 测试覆盖 JSONL 分帧（含 U+2028 不被切分、末尾半行、超限）、分支分页、断链/重复 ID/自环、越界 cwd、符号链接逃逸、鉴权、Host/Origin、requestId 防重、未实现方法拒绝、并发写入、背压、空闲回收、进程退出

## 真实 Pi 冒烟

```bash
go build -o /tmp/pi-bridge ./cmd/pi-bridge
export PI_BRIDGE_TOKEN="0123456789abcdef0123456789abcdef"
/tmp/pi-bridge --workspace /srv/projects/pi \
  --pi "$(command -v pi)" \
  --state-dir /tmp/pi-bridge-smoke \
  --idle-timeout 30s --max-workers 1 &
BRIDGE=$!
sleep 1
# 握手：只查询状态，不发送任何提示词
printf '%s\n' '{"version":1,"kind":"command","requestId":"smoke-1","method":"worker.list"}' | \
  websocat -H "Authorization: Bearer $PI_BRIDGE_TOKEN" ws://127.0.0.1:30142/api/v1/ws
kill -TERM $BRIDGE; wait $BRIDGE 2>/dev/null
```

冒烟标准：握手成功、`worker.list` 返回空表、桥退出后没有残留的 `pi` 进程。**不要在这个阶段发送真实提示词。**

## 已知限制

- A 阶段进程监督仅实现 Linux（独立进程组 + `Pdeathsig`），其他平台显式报错
- 同一桥内保证一个会话只有一个写入进程；**不解决**与外部 `pi` CLI 的文件级互斥
- 历史读取是请求内扫描，超大会话受 `Limits` 约束；磁盘索引属于后续阶段
- 事件没有补发：断线后需重新读取持久历史，等权威 `message_end`
- 连接内 `requestId` 防重有上限（1024），跨重启的 exactly-once 未实现
- 云端 relay 未实现，A 阶段只能本机环回访问
