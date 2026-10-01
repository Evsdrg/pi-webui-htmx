# 开发说明（桥）

> 面向使用者的说明见 [../README.md](../README.md)；本文件是桥的开发细节与内部记录。

本地 Go 桥连接浏览器与独立 `pi --mode rpc` 子进程。目标是适配 HTMX 工作台、直接读取 Pi 数据、并使 agent 内存随进程退出释放。桥不嵌入 Pi SDK，不另建一份会话正文数据库。

**仓库边界：** 本仓只含 Go 桥。相邻的 `../pi-webui-htmx`（HTMX 前端与 UI 包）是独立 git 仓库；二者没有共同父仓库，也不要为它们建一个总仓库。上游 `pi-web` 的只读参考检出在 `../../src-read-only/pi-web`（不在 `pi/` 下）。跨仓改动分两边提交，配套关系写在 [UI 包契约](../../pi-webui-htmx/docs/contract.md)。

**状态：** 本地工作台与 relay/tunnel 后端均已实现，云端形态的整链路（relay 设备前缀 → 隧道 HTTP 帧 / WS 别名 → 桥）已端到端跑通并实测。台账 B01–B81、U01–U21、T01、D01–D02 在此前多轮修复中逐项关闭；剩余的**部署约束**与**未经真实环境验收的部分**见 [剩余问题联合分析](remaining-issues-plan.md)，逐项证据见 [code-audit.md](code-audit.md)。

## 文档入口

| 文档 | 内容 |
|---|---|
| [剩余问题联合分析与实施](remaining-issues-plan.md) | 批次 A–J、影响矩阵、依赖与最终状态（**当前**） |
| [架构与修复决策](architecture.md) | S01–S12 设计、取舍与**逐节实施状态表** |
| [整体实施规划（历史）](repair-plan.md) | 更早一版的 P0–P7 依赖与回归门槛，仅作参考 |
| [通信约定](communication.md) | HTTP/WS/Pi 分层、作用域、受理/恢复和背压 |
| [当前 v1 协议](../api/v1/protocol.md) | 已有入口/方法与目标语义的区别 |
| [Pi 兼容矩阵](pi-compatibility.md) | Pi 0.85.1、桥、UI 和刻意排除/暂缓项 |
| [审查台账](code-audit.md) | 问题、证据、方案归属与待修状态 |
| [Go 惯用写法复核](go-idioms-review.md) | G01–G19：错误链、类型表达、结构体量与工程配置的对照结论 |
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
  --workspace /srv/projects/pi \
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
- `/api/v1/capabilities`：当前版本、方法和限额（B80 的硬编码待修）。
- `/api/v1/sessions`、`/api/v1/sessions/{id}/history`：只读磁盘，不启动 Pi。
- `/api/v1/ws`：命令、回执、事件；`/ui/*`：HTMX 片段及受控资源。

`requestId` 是请求关联/去重键，`epoch/seq` 是传输游标，`entryId` 是持久历史标识。prompt accepted 不等于完成；timeout/outcome_unknown 不能自动重发。浏览器断开不等于取消已受理工作。

## 修复方向与落地状态

下面这些方向在批次 A–J 中逐项落地（台账 107 项中 105 项已修，保留 B40/B71 两项部署侧事项）：

1. 本地/tunnel 共用 Executor 与预算 ✅；持久去重与 unknown 恢复 ✅；PTY、订阅、安全控制分别处理 ✅。
2. replay 与 live 注册原子化 ✅；连接拥有订阅与取消资源 ✅；身份变化换 epoch ✅。
3. Manager 用稳定 workerID 与 session 预留完成身份事务 ✅；删除前收敛 writer ✅。
4. 同一配置 schema 处理数组/脱敏/恢复 ✅；revision、秘密保留、默认拒绝重定向 ✅；
   禁止远程新增 `!command` 凭据表达式 ✅。
5. 历史/tree/title/lazy 共用验证后的偏移索引 ✅；大内容走 HTTP ✅；控制帧保持小 ✅。
6. UI SessionScope 在 htmx 处理响应前拦截旧结果 ✅；固定命令目标 ✅。
7. relay 持久身份、TTL、部署信任与连接回收 ✅；同源 HTTP+WS 设备路由 ✅。

逐项证据（含反例名与文件）见 [code-audit.md](code-audit.md)，批次与影响矩阵见
[remaining-issues-plan.md](remaining-issues-plan.md)。

## 模型配置与执行边界

`config.models.*` 由前端触发；桥提供原语。Pi 的 models 是数组，api 是协议标识，baseUrl 才是 HTTP 地址。当前 raw/write 的秘密保护、摘要计数和固定临时文件有已知缺陷；不能将“临时文件+rename”概括为所有安全/并发问题已解决。

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

2026-09-27 的 P0 本地联测：Go race/vet/gofmt、UI 72 项、typecheck、build、check 通过；首屏 gzip 36.64 KiB。两仓独立 CI 已配置但托管运行尚未验证。跨仓验证必须显式设置 `PI_WEBUI_DIR` 并运行 `scripts/verify-pair.sh`，缺 UI 直接失败；[方法与入口清单](method-inventory.md) 和 Go/TS/模板静态契约同时检查。其他审查反例仍须随各项修复进入正式回归测试。

## 资源、压缩与已知限制

- 2026-09-26 隔离、无扩展、未发送 prompt 的历史测量：桥约 10.2 MiB RSS，Pi 约145 MiB。这不是本轮新测量，不能代表长会话/启用插件或与不同工作负载 Pi Web 的公平对比。
- 桥已经有 br/gzip 和有界资产缓存；qvalue、identity/Vary 与资产命中已按 B39/B60/B61 修正。产物 gzip 预算是**首屏自有代码**的额度，不是实际页面传输量。
- History 的扫描缓存有多槽与文件身份校验（dev+ino），同 size/mtime 替换不再误命中；标题、lazy 与 tree 共用同一份扫描产物（B12/B37/B38/U04）。
- 当前 Linux 正常停止采用进程组（`Test停止回收同组后代` 覆盖）；Pdeathsig 不保证桥被 SIGKILL 后所有后代消失，生产模式使用受监督的 systemd cgroup（见「受管部署」），手工启动明确为较弱保证。
- 非 Linux PTY 当前有编译缺口，不能宣传为完整可构建的显式拒绝路径。
- 跨桥/外部 CLI 的非合作写入不受桥内互斥保证。

OAuth/额度查询、插件远程安装等排除项，以及 Web Push 暂缓、PWA/版本检查等未立项项，统一见兼容矩阵；不要把“未实现”自行改写成“用户不要”。
