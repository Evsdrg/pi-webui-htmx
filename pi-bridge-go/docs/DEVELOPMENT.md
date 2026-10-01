# 开发说明（桥）

> 面向使用者的说明见 [../README.md](../README.md)；本文件是桥的开发细节与内部记录。

本地 Go 桥连接浏览器与独立 `pi --mode rpc` 子进程。目标是适配 HTMX 工作台、直接读取 Pi 数据、并使 agent 内存随进程退出释放。桥不嵌入 Pi SDK，不另建一份会话正文数据库。

**目录结构：** 本目录是 Go 桥；相邻的 `../pi-webui-htmx` 是 HTMX 前端与 UI 包，二者在同一仓库内并列，跨仓改动分别提交并同时通过。配套契约见 [UI 包契约](../../pi-webui-htmx/docs/contract.md)。

**状态：** 本地工作台与 relay/tunnel 后端均已实现，云端形态的整链路（relay 设备前缀 → 隧道 HTTP 帧 / WS 别名 → 桥）已端到端跑通并实测。未经真实环境验收的部分见 [architecture.md](architecture.md) 的「实现状态」。

## 文档入口

| 文档 | 内容 |
|---|---|
| [架构](architecture.md) | S01–S12：目标形态、边界约束与关键设计决策 |
| [通信约定](communication.md) | HTTP/WS/Pi 分层、作用域、受理/恢复和背压 |
| [当前 v1 协议](../api/v1/protocol.md) | 入口、方法、事件与限额 |
| [Pi 兼容矩阵](pi-compatibility.md) | Pi 0.85.1 的能力对照与刻意排除项 |
| [方法清单](method-inventory.md) | 方法分类（由测试与代码交叉校验） |
| [技术栈](tech-stack.md) | 依赖与版本清单 |
| [许可证核查](licensing.md) | 依赖兼容性结论 |
| [UI 包契约](../../pi-webui-htmx/docs/contract.md) | 模板/构建/前端行为与版本配套 |

## 当前能力

| 状态 | 能力 |
|---|---|
| ✅ 已接线 | 显式创建/恢复、发送/取消、模型/思考切换、压缩/重试、分支操作、bash |
| ✅ 已接线 | 只读会话列表/历史、搜索、惰性思考/图片、文件/Git、PTY、扩展对话 |
| ✅ 已接线 | 模型配置原始 JSON 编辑、discover/test、包版本只读清单 |
| ⚠️ 有实现但有审查缺陷 | 持久去重、重放、身份变更、relay 凭据/配对、资源预算及配置保护 |
| ⚠️ 未完成解耦 | 历史树、HTML 导出当前仍需要 worker |
| ❌ 尚未接通 | 云浏览器经 relay 使用完整 HTMX/图片/上传/下载工作台 |

具体方法查 capabilities.methods 和协议清单。方法存在、基线测试通过，均不代表台账中的边界已经修好。

## 本地运行

先在相邻 `pi-webui-htmx` 执行 `pnpm install --frozen-lockfile && pnpm build`，再在本仓运行：

```bash
export PI_BRIDGE_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/pi-bridge \
  --workspace "$HOME/projects" \
  --listen 127.0.0.1:30142 \
  --pi "$(command -v pi)" \
  --state-dir "$HOME/.local/state/pi-bridge" \
  --ui-dir ../pi-webui-htmx \
  --idle-timeout 2m --max-workers 4
```

- `--workspace` 是授权工作区；本地桥当前要求环回监听，不直接暴露公网。
- `--pi`、额外运行参数由本机运维配置，不接受网页选择。
- `--state-dir` 保存桥状态、隔离会话和临时资源；长期使用不要放 `/tmp`。
- `--agent-dir` 可指定 Pi 配置；默认隔离目录避免自动使用真实用户配置。共享生产会话目录不能解决与外部 CLI 同时写的问题。
- 扩展默认关闭；显式开启 `--extensions` 也不等于授权全部项目执行，更不提供沙箱。
- 不配置 `--ui-dir` 时只提供 API；配置后必须有模板和 Vite 产物，没有内嵌/CDN 兜底。UI rebuild 后重启桥，确保模板、manifest 与哈希资源一致。

### 受管部署（systemd）

桥被 SIGKILL 时 `Pdeathsig` 只覆盖直接子进程——忽略 SIGTERM 的 shell 或扩展
后代仍会存活。生产用法是让服务管理器负责整个单元：

```ini
[Service]
KillMode=control-group
TimeoutStopSec=30
```

`KillMode=control-group` 让 systemd 在停止单元时向 cgroup 内全部进程发信号，
补上桥自己做不到的那一半（正常 Stop 会向整组发信号，见 `Test停止回收同组后代`）。
手工 `go run` 仍是较弱保证。

## 核心接口

- `/healthz`：健康状态；`/api/v1/auth`：Bearer 换 Cookie。
- `/api/v1/capabilities`：当前版本、方法和限额（限额引用运行时真实配置）。
- `/api/v1/sessions`、`/api/v1/sessions/{id}/history`：只读磁盘，不启动 Pi。
- `/api/v1/ws`：命令、回执、事件；`/ui/*`：HTMX 片段及受控资源。

`requestId` 是请求关联/去重键，`epoch/seq` 是传输游标，`entryId` 是持久历史标识。prompt accepted 不等于完成；timeout/outcome_unknown 不能自动重发。浏览器断开不等于取消已受理工作。

## 关键不变量

改这些部分之前先读对应设计（[architecture.md](architecture.md) 的 S01–S12）：

1. 本地与 tunnel 共用同一 Executor：准入、去重、预算与超时按方法声明，不按连接类型分叉。
2. replay 与 live 注册在同一锁内完成，快照末尾序号等于注册序号；身份变化换 epoch，游标不跨 epoch 接受。
3. worker 身份是事务：先预留、后调 Pi、再按真实 ID 提交；删除前先收敛 writer。
4. 配置写入按 revision 校验，`***` 是占位符而非凭据；网页不能新增 `!command` 形式的凭据表达式。
5. 历史、tree、标题、lazy 共用一份带文件身份校验的偏移索引；大内容走 HTTP，控制帧保持小。
6. UI 在 htmx 处理响应之前拦截过期响应（目标、会话代次、局部 revision 三重守卫）。
7. relay 只转发不解析；设备前缀同时承载 HTTP 与 WS，鉴权始终在桥自己的 handler 上执行。

## 模型配置与执行边界

`config.models.*` 由前端触发；桥提供原语。Pi 的 models 是数组，`api` 是协议标识，`baseUrl` 才是 HTTP 地址。写入按 revision 校验、按 model id 合并、把 `***` 识别为占位符保留原值；临时文件为同目录独占创建 + Sync + rename。这些保护覆盖桥自己的写入路径；外部 CLI 的不合作并发写入不受桥控制。

config.packages 只读清单与版本，不安装/更新。只读 Git 同样要防 fsmonitor/external diff/textconv 等隐式执行。Pi 工具和显式 PTY 本身具有执行能力；工作区根与环境过滤不是对它们的系统隔离。

## 部署形态

三种形态，桥的代码与 UI 产物完全相同，差别只在浏览器怎么到达它：

| 形态 | 浏览器访问 | 适用 |
|---|---|---|
| 本地直连 | `http://127.0.0.1:30142/` | 本机开发与单机使用 |
| 云端 relay | `https://relay.example.com/d/{deviceId}/` | 桥在 NAT 后、无入站端口；桥主动外连 relay |
| 反向代理 | `https://your.domain:port/` | 已有反代（Caddy/nginx）与隧道（如 EasyTier）的部署 |

**云端 relay 形态**：relay 做用户认证与设备归属校验，把请求封装成隧道帧发给桥；
桥在**自己的 HTTP handler** 上执行，响应按 ID 配对回传。设备前缀同时承载 HTTP 与 WS
（`/d/{id}/api/v1/ws`），前端全部使用相对路径，因此同一份产物在三种形态下都指向正确的前缀。

relay 启动的两条硬约束（都在启动期失败，不留到运行期）：

- `--host` **必填**，值是精确的 Host 头（`relay.example.com`，或带端口 `relay.example.com:30143`
  表示精确匹配；不带端口则忽略请求端口）。没有它就不允许启动——旧行为在 `--host` 为空时
  把 Host 与 Origin 两道校验一起跳过（B63）。
- `--state-dir` 默认是 `$XDG_STATE_HOME/pi-relay`（回退 `~/.local/state/pi-relay`）；
  设备注册表与用户表是持久身份，显式指到临时目录时启动会打 WARN（B55）。

**反向代理形态**：桥侧用 `--public-origin`（或 `--listen` 绑非环回 + 显式来源）声明外部地址，
Host 与 Origin 仍严格核对，只是多一个「声明过的外部来源」；cookie `Secure` 跟随该来源。
反代必须保留原始 Host 转给桥，WS 升级才能通过。

**未验收**（不冒充已验）：真实模型在云端形态下的长时流式、公网 relay 的跨机 RTT 与丢包、
小时级长稳、证书轮换。TLS relay 可见转发明文；不落盘不等于端到端加密。

## 测试与证据

```bash
go vet ./...
go test -race ./...
PI_WEBUI_DIR=../pi-webui-htmx go test -race ./internal/transport/
```

| 位置 | 用途 |
|---|---|
| internal/**/*_test.go | 与生产代码同包的单元/集成测试 |
| internal/testutil | 跨包辅助；假 Pi 按需构建 |
| testdata/fake-pi | 可控夹具源码，不提交编译产物 |
| tools/smoke-client | 手工真实 Pi 冒烟，不属于自动付费模型测试 |

FakePi 已按测试进程使用独占构建目录，TestMain 在测试结束后清理，见 T01。真实 Pi 冒烟使用隔离配置，只做握手/状态/退出，不加载生产秘密或发送付费请求。

跨仓验证必须显式设置 `PI_WEBUI_DIR` 并运行 `scripts/verify-pair.sh`，缺 UI 直接失败；[方法清单](method-inventory.md) 由 `ui_contract_test.go` 与 Go 注册表、UI 类型、模板交叉核对，此外还有 Go/TS/模板静态契约。

## 资源、压缩与已知限制

- 2026-09-26 隔离、无扩展、未发送 prompt 的历史测量：桥约 10.2 MiB RSS，Pi 约145 MiB。这不是本轮新测量，不能代表长会话/启用插件或与不同工作负载 Pi Web 的公平对比。
- 桥已经有 br/gzip 和有界资产缓存；qvalue、identity/Vary 与资产命中已按 B39/B60/B61 修正。产物 gzip 预算是**首屏自有代码**的额度，不是实际页面传输量。
- History 的扫描缓存有多槽与文件身份校验（dev+ino），同 size/mtime 替换不再误命中；标题、lazy 与 tree 共用同一份扫描产物（B12/B37/B38/U04）。
- 当前 Linux 正常停止采用进程组（`Test停止回收同组后代` 覆盖）；Pdeathsig 不保证桥被 SIGKILL 后所有后代消失，生产模式使用受监督的 systemd cgroup（见「受管部署」），手工启动明确为较弱保证。
- 非 Linux PTY 当前有编译缺口，不能宣传为完整可构建的显式拒绝路径。
- 跨桥/外部 CLI 的非合作写入不受桥内互斥保证。
- 压缩器：动态与静态资源统一 `lgwin=19`（实测与本项目响应体积下 `lgwin=22` 输出逐字节相同，而单 writer 常驻从 9.9 MiB 降到 2.9 MiB）；静态资源压缩串行化并把 ≥256 KiB 的 chunk 降到 quality 7，并发 3 个大 chunk 的峰值从 121 MB 降到 50 MB 左右。
- 未设 `SetMemoryLimit`/`SetGCPercent`：小内存 VPS 部署建议显式限制。

## 后续可做的优化（未实施）

以下是已测量、尚未实施的候选，按收益/风险排序。数字来自隔离原型（冻结的真实会话副本、环回、假 Pi），不是产品实现的收益承诺。

| 候选项 | 问题 | 方向 |
|---|---|---|
| 合并消息投影的重复解码 | `sessions` 投影对同一条 message 多次解码（外层、块类型、用量、工具状态各一次） | 共用一份解码结果；须对错误类型、null、缺失字段、未知 role 建差分测试——原型曾因共用结构让整条 message 被错误类型清零 |
| 收起态预览有界 | 工具块收起时仍把完整正文渲染进预览（实测占页面 96.8%，而 CSS 是单行省略） | 只为预览截断（约 200 字符）并只遍历边界内 UTF-8；详情、复制、导出不得截断 |
| 惰性内容按索引定位 | thinking/图片展开仍从头读文件（实约 146 ms） | 复用扫描索引的 offset/size 做 ReadAt（原型约 0.3 ms）；必须校验返回记录 ID 与请求一致 |
| 小型多会话缓存 | 单槽扫描缓存在会话交替浏览时反复重扫（A→B→A 约 112–131 ms） | 2–4 槽覆盖切换；保留 size/mtime/identity 失效校验，先建立可信的保留内存预算 |

OAuth/额度查询、插件远程安装等排除项，以及 Web Push 暂缓、PWA/版本检查等未立项项，统一见兼容矩阵；不要把“未实现”自行改写成“用户不要”。
