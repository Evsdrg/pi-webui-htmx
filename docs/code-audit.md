# 代码审查记录与修复方案索引

更新：2026-09-30（第二轮修复已落地，见下方两段复核记录）。**P0 已完成，P1 正在实施，协议仍为 v1。** 下表 ✅ 表示对应代码与回归已完成，未标记项仍待修复。技术方案见 [architecture.md](architecture.md)，批次与进度见 [repair-plan.md](repair-plan.md)。

## 联合复核：当前优先入口

后续整体核查见 [剩余问题联合分析与实施顺序](remaining-issues-plan.md)。原表实际106个编号、17项未关闭（含B71部署约束），不是上一轮汇报的15项。此次重开B63、U16、B77；B70校正为仅目录/预设未接线，R01记录图片URL/分支监听释放遗漏。旧阶段总表与下方限额表仍含历史状态，不作为当前验收保证；以逐条源码证据和联合方案为准。

## 范围与基线

审查对象：`pi-bridge-go`、`pi-webui-htmx`；以 `/srv/projects/src-read-only/pi-web` 的当前源码作对照。覆盖两个仓库的生产 Go 模块、HTMX/TypeScript 入口与模块、模板、协议和主要资源生命周期；对会话、凭据、命令与事件传输、工作区、Git、终端、relay、压缩和前端异步切换做了重点源码核对与定向反例。本文是源码审查记录，不是形式化证明；运行时不能安全或稳定触发的条目会明确标为源码确认。

审查基线：本轮开始时桥 `go test -race ./...`、`go vet ./...` 通过；前端 72 项 Vitest、TypeScript、生产构建及 `pnpm check` 通过。另有 16 个桥侧和 5 个前端初始审查反例未通过，后续定向探针继续新增发现；这些是审查探针结果，不是基线测试回归。探针存放在 `/srv/projects/agentTmp/pi-audit-vxcvho/`，未留在产品仓库。Pi Web checkout 未安装完整依赖；`npm test` 的 530 项中 115 项因缺少 `jiti`、`react`、Pi SDK 等依赖及平台相关路径用例失败，因此不将其测试结果当作产品代码质量结论。


### 2026-09-30 逐条复核

对上一版标为「未修 / 部分」的条目逐条回到代码核对，结论如下。**状态列已按核实结果改写**，所以不要再把「未修」当作默认假设。

- **实为已修（上一版状态过时）**：B31、B32、B59、B63、B64、B74——各自给了核实依据。
- **本轮修完**：B66、B72、B73、B78、B79。
- **描述需要修正**：B43（超大文件只 stat 不读内容，原文夸大了 I/O 放大）、B73（真正无锁的是 `terms` 而不是 `subs`，且附带一处会锁死整个 TunnelBridge 的 `acquire` 死锁）。
- **缺口比原文更大**：B54（relay 侧缺设备前缀 HTTP 转发，前端缺 basePath，S09 目标形态两端都未实现）。
- **复核后仍确认未修**：U04、U15、U16、U18、B33、B36、B37、B38、B40、B43、B45、B50、B51、B53、B54、B55、B70、B75、B76、B77、B80、D01。

### 2026-10-01 第三、四批修复

- **本轮修完**：U15、U16、U18、B36、B51、B43，并补了图片 blob URL 与分支监听的释放（R01）。
- **D 批（共用磁盘读取）**：B37（标题两端读）、B38/O03（惰性读取复用扫描索引）、O04（扫描缓存 4 槽 + 总预算 + LRU 淘汰）、U04（磁盘树投影）。
- **B63/U16/B77 曾标为已修但复核后重开**：B63 当时误用本地桥的 public-origin 校验当作 relay 已修的证据；U16 的确认帧仍缺会话代次守卫；B77 仅做导出前裁剪，不含单文件与并发硬配额。三项在 [remaining-issues-plan.md](remaining-issues-plan.md) 登记后重新打开。

这轮复核本身也说明一件事：**台账状态会漂移**，判断某个问题是否还存在时，先看代码，别只看这里的状态列。

### 2026-09-30 第二轮修复

在第一次复核之后又处理了一批：

- **本轮修完**：B33、B50、B53、B77、B80、U16。
- **复核后确认已修**：B34（README 已更新状态段）。
- **部分完成**：D01——前端文档里的「Vite 7 / 无前端测试」已不成立，本轮只更新了前端 README 的数字与桥侧 protocol.md 的能力描述。
- **B54 仍待立项**：relay 侧缺按设备前缀转发 HTTP 的入口，前端也没有 basePath，S09 的目标形态两端都未实现，属于 P7。

两轮复核共改写 19 条状态。**这些状态仍然会漂移**：判断某个问题是否还存在时，先看代码。

## 代码审查发现

严重性：`高` 表示凭据/数据、重复副作用、主要交互或远程资源边界受影响；`中` 表示特定会话形状或恢复路径受影响。编号供后续修复与验证追踪。

| ID | 严重性 | 项目 | 问题与影响 | 主要位置 |
|---|---|---|---|---|
| B01 | ✅ 已修 | Bridge | 统一配置遍历器保护所有自定义头部值，并区分 provider/model 身份键与字段名；Raw/Models 共用脱敏。回归覆盖未知头名与特殊 provider 名。 | `internal/management/config_values.go`；`config_safety_test.go` |
| B02 | ✅ 已修 | Bridge | 秘密按 provider、模型 ID、override 键及大小写无关头部名恢复；重排不串值，不修改调用方对象。无来源/歧义占位符拒绝，明确新值可修复旧坏配置。 | `internal/management/config_values.go`；`config_identity_test.go` |
| B03 | ✅ 已修 | Bridge/UI | **修复：** 模板 radio 值、`queueKind()` 与 `refreshQueueState` 选择器统一为协议值 `steering`/`followUp`；桥对旧值 `steer` 给出可操作提示。回归同时锁定 wire 值与回读定位，两个反例均稳定失败。 | `pi-webui-htmx/src/{templates/shell.html,modules/workbench.ts}`；`internal/runtime/session_ops.go` |
| B04 | ✅ 已修 | Bridge | **修复：** 桥级 claim 注册表 + 命令指纹，本地 WS 与隧道虚拟连接共用同一 `admit`。64 并发压测验证只有一个放行；在途登记绝不被淘汰。原问题：`seen` 只在单连接内，两连接可同时执行同一 requestId。 | `internal/transport/claims.go`；`methods_test.go` |
| B05 | ✅ 已修 | Bridge | **修复：** `SubscribeWithReplay` 在单次持锁内完成「取快照 + 注册订阅」，并把 WS 与隧道两条入口的订阅逻辑收敛成一个共用实现。回归断言「快照末序号 == 注册序号」，反例（拆成两次加锁）5/5 稳定失败。 | `internal/runtime/manager.go`；`internal/transport/server.go`；`manager_test.go` |
| B06 | ✅ 已修 | Bridge | **修复：** 超限帧不再杀死与 Pi 的连接——用已读头部定位调用方、吞掉行尾保持流对齐，只让那一条命令失败并返回明确错误。回归用回环 fake 验证第二条命令仍成功；反例（超限即 fail）稳定失败。原 9 MiB 探针的归因已按源码纠正为桥自身 8 MiB 帧上限。 | `internal/pi/client.go`；`internal/jsonl/reader.go`；`client_test.go` |
| B07 | ✅ 已修 | Bridge/UI | **修复：** 大内容改走 HTTP `/ui/file-text`（不依赖 UI 包、支持压缩）；WS 版 `files.read` 加 448 KiB 安全预算，超限截断并标记 `truncated`，不再把超限帧交给连接层。前端优先 HTTP、失败才退回 WS 预览。 | `internal/transport/server.go`；`pi-webui-htmx/src/modules/workspace.ts` |
| B08 | ✅ 已修 | Bridge | **修复：** 删除前先 `StopSession` 停掉该会话 worker，失败时非 force 明确拒绝、force 下仍尽力再停；结果带 `stoppedWorker`。原问题：worker 忙时也能删文件，Pi writer 仍存活。 | `internal/runtime/manager.go`；`internal/transport/server.go` |
| B09 | ✅ 已修 | Bridge | **修复：** 新增 `sessionIDFromFileName`，取下划线后最后一段，无下划线时退回整个 basename。回归覆盖标准命名、裸 ID、无扩展名与畸形尾段。 | `internal/runtime/identity.go`；`identity_test.go` |
| B10 | ✅ 已修 | Runtime | **修复：** 退出清理改为按「当前映射」删除，不再用启动时捕获的 sessionId。回归验证 fork 后回收时注册表彻底清空；反例下 `forked-fake-session` 永久残留。 | `internal/runtime/manager.go`；`identity_test.go` |
| B11 | ✅ 已修 | Bridge/UI | **修复：** `Turn.Thinking` 改为 `[]ThinkingBlock`（自带 entry ID + 块下标），不再用回合级单一 `AssistantEntryID` 配合并下标。回归覆盖同回合多 assistant、孤儿 assistant 与无思考块三种形态。 | `internal/presentation/presentation.go`；`pi-webui-htmx/src/templates/history.html` |
| B12 | ✅ 已修 | Sessions | **修复：** 缓存键加入文件身份（dev+ino，平台适配）；取不到身份时一律不命中、也不写入。回归用 rename + 保留 mtime + 等长替换复现原缺陷。 | `internal/sessions/cache.go`；`identity_lin.go`；`cache_test.go` |
| B13 | ✅ 已修 | Sessions | **修复：** 快路径补 `balancedJSON` 结构配平校验（O(n)、零分配），覆盖未被本页选中的损坏记录；反向用例确认含转义引号与嵌套括号的合法记录不误伤。 | `internal/sessions/head.go`；`scan.go`；`scan_test.go` |
| B14 | ✅ 已修 | Tunnel | **修复：** subs 保存真实订阅句柄，退订与重复订阅 Close 旧订阅；同一虚拟连接的订阅/退订按接收顺序执行，防止退订抢在登记前。回归每轮即时核对 12 对操作的 worker 订阅数为 1→0。 | `internal/transport/tunnel.go`；`tunnel_test.go` |
| B15 | ✅ 已修 | Tunnel | **修复：** 隧道命令与本地 WS 共用 `admit` 与全局 `operations` 预算。原问题：隧道路径完全绕过桥级并发上限。 | `internal/transport/tunnel.go`；`claims.go` |
| B16 | ✅ 已修 | Bridge | **修复：** 先校验参数再摘除对话；回执送达失败时把对话还回等待表。回归覆盖「非法回执后可合法重试」。 | `internal/runtime/dialogs.go`；`dialogs_test.go` |
| U01 | ✅ 已修 | UI | **修复：** `selectSession` 切换时清空附件，附件不再跨会话残留；发送进行中仍保留输入以便重发。 | `src/modules/workbench.ts`；`tests/unit/workbench.test.ts` |
| U02 | ✅ 已修 | UI | 发送前固定用户所选模型，不受 `ensureWorker()` 内的状态刷新覆盖；Pi 恢复模型为 `unknown/unknown` 时显示历史标识为不可用、保留草稿并阻止误发。真实会话隔离副本与前端回归均覆盖。 | `pi-webui-htmx/src/modules/workbench.ts`；`internal/sessions/store.go` |
| U03 | ✅ 已修 | UI | **修复：** 新增 `SessionScope`；`command()` 固定发起时归属的会话，切换后不再改投。 | `src/modules/scope.ts`；`src/modules/workbench.ts` |
| U04 | ✅ 已修 | UI/Bridge | 本轮修复：新增 `Store.Tree` 从磁盘投影会话树（形状与 Pi `get_tree` 对齐，迭代摊平防深链栈溢出），`/ui/branch` 无 worker 时走它；有 worker 时仍用 Pi 实时树。差异如实标出：不含内存态、fork 列表为空（fork 仍是写操作）。反例：`branch_disk_test.go`——无 worker 时片段含 `data-branch-goto`、不启动进程、不再提示「请先显式启动会话」。 | `internal/sessions/tree.go`；`internal/transport/server.go`；`branch_disk_test.go` |
| U05 | ✅ 已修 | UI/Bridge | **修复：** WS 读上限从 1 MiB 提升到与附件预算对齐（`pi.MaxImages × pi.MaxImageDataLen + 1 MiB`），前端发送前按 base64 总量预检并给出可读错误，不再以断线形式失败。 | `internal/transport/server.go`；`pi-webui-htmx/src/modules/attachments.ts`；`src/modules/workbench.ts` |

## 继续审查发现（源码核对/定向复现）

以下不是上述 21 个反例的重复计数；来源为单独的本地探针或对具体执行路径的源码核对。尚需在最终报告中区分可重复的运行时复现与静态路径确认。

| ID | 严重性 | 项目 | 问题与影响 | 证据/主要位置 |
|---|---|---|---|---|
| B17 | ✅ 已修 | Bridge | 供应商请求使用拒绝重定向的独立 client；两个本地服务器的回归确认目标不接收凭据。并拒绝超限正文、禁止错误正文回显秘密。 | `management/discovery.go`；`discovery_safety_test.go` |
| B18 | ✅ 已修 | Bridge | **修复：** 统一 runner 清理环境、禁用已知 helper，转换过滤器明确拒绝；五类标记脚本回归通过。这不是任意 Git/恶意本机进程的 OS 沙箱。原问题：Git 查询执行仓库配置中的外部命令：`git status` 触发 `core.fsmonitor`，`git diff` 触发 `diff.external`。两个本地标记脚本探针均复现；Pi Web 的 status 路径也调用普通 `git status`，但它的 diff 明确带 `--no-ext-diff`。 | `workspace/git.go`；`pi-web/lib/git-changes.ts`；`git-probe.log`；`git-diff-probe.log` |
| B19 | ✅ 已修 | Relay | **修复：** 用户表落盘到 `state-dir/users.json`（原子替换、只存哈希），`NewUsers` 启动时加载；文件损坏明确报错而非静默清空。 | `internal/relay/users.go`；`cmd/pi-relay/main.go` |
| B20 | ✅ 已修 | Relay | **修复：** `Claim` 校验 `PairingAt` 与 `ClaimTTL`，过期即作废并拒绝；`PairingAt` 仅存内存，重启后未使用码一律失效。 | `internal/relay/registry.go`；`registry_safety_test.go` |
| B21 | ✅ 已修 | Relay | **修复：** 尝试表先清理过期项再查活跃硬上限（1024），超出即拒绝，不再依赖滚动驱逐兜底。 | `internal/relay/registry.go` |
| B22 | ✅ 已修 | Relay | **修复：** `persist` 在持锁状态下完成序列化，解锁后只做文件写入；`-race` 竞争探针已验证。 | `internal/relay/registry.go` |
| B23 | ✅ 已修 | Relay | **修复：** 每 owner 浏览器连接上限 16，与关闭状态、设备在线在同一段持锁区间内判定；另一设备的连接不受影响。 | `internal/relay/server.go`；`relay_limits_test.go` |
| B24 | ✅ 已修 | Tunnel/Relay | **修复：** 桥侧隧道令牌改走 `Authorization: Bearer` 头；relay 优先取头、查询串保留为兼容回退。 | `internal/tunnel/client.go`；`internal/relay/server.go` |
| B25 | ✅ 已修 | Bridge | **修复：** porcelain -z 同时读取分支与状态；空仓库、特殊文件名、重命名回归通过。原问题：合法的 unborn/空 Git 仓库没有 `HEAD`；`GitStatus` 先执行 `rev-parse --abbrev-ref HEAD` 并把失败作为整次查询失败。空仓库本地探针复现。 | `internal/workspace/git.go`；`git-probe.log` |
| B26 | ✅ 已修 | Bridge | **修复：** NUL 增量读取，2 MiB/50000 条上限，溢出取消整组，缓存传播 truncated；9000 长文件名回归通过。walk/大结果传输仍按 B27/S06 推进。原问题：`files.index` 的 Git 路径用 `cmd.Output()` 完整捕获 `git ls-files`，之后才应用 50000 条上限；超大仓库会先无界分配输出和 `strings.Split` 切片。 | `internal/workspace/index.go` |
| B27 | ✅ 已修 | Bridge | **修复：** 目录列表改为流式分批 `ReadDir(128)`，边读边按上限截断；排序仍在截断后的切片上做，规则不变。 | `internal/workspace/files.go`；`files_test.go` |
| B28 | ✅ 已修 | Sessions | **修复：** 搜索遇超大行跳过该文件时置 `Truncated`，不再向调用方报告「结果完整」。 | `internal/sessions/search.go`；`search_test.go` |
| B29 | ✅ 已修 | Sessions | **修复：** TTL 内的有效性改用顶层目录轻量戳（文件数 + 最新 mtime），不再遍历整棵树算指纹；戳随索引一起更新。 | `internal/sessions/index.go`；`index_test.go` |
| B30 | ✅ 已修 | Bridge | **修复：** 有副作用命令派发前可靠写 pending intent（带指纹），完成后落终态；重启见到 pending 只回答 `outcome_unknown`，不假装成功。时序测试用会阻塞的假 sink 捕获「派发中已在盘上」。 | `internal/transport/claims.go`；`methods_test.go` |
| B31 | ✅ 已修 | Bridge | 复核（2026-09-30）：`requestFingerprint` 已包含 method、sessionId 与规范化 params，不同命令误用同一 requestId 会得到 `conflict`，不会拿到旧命令的 `duplicate` 回执。 | `internal/transport/server.go`；`internal/storage/receipts.go` |
| B32 | ✅ 已修 | Bridge | 复核（2026-09-30）：`maxPackages=128`、`maxPackageQueries=4`、`maxPackageMetadata=1 MiB` 都已在 `packages.go` 生效，跨请求并发有上限。 | `internal/management/packages.go` |
| B33 | ✅ 已修 | Bridge | 本轮修复：前端图片预览改走 `/ui/file-image`（HTTP 回原始字节，不再 base64 展开、不再占用 WS 帧预算）；桥侧 `files.image` 在 base64 超过 `wsTextBudget` 时明确回 `limit_exceeded` 并指出该端点，不再把超限帧交给连接层静默丢弃。反例：`transport/file_image_test.go` 两条 + 前端 `workspace_image.test.ts` 两条。 | `internal/workspace/files.go`；`internal/transport/server.go` |
| B34 | ✅ 已修 | Bridge | 复核（2026-09-30）：README 已无「A 阶段」字样，本次把状态段更新为「P1–P6 已按批次落地、云端整链路未实现（P7）」并写明台账状态会漂移。 | `pi-bridge-go/README.md`；`internal/transport/server.go`；`api/v1/protocol.md` |
| B35 | ✅ 已修 | Relay | **修复：** scheme 推断信任 `X-Forwarded-Proto`（仅接受明确 https，其余按 http），Cookie `Secure` 同步跟随；无代理头时仍按 r.TLS。 | `internal/relay/server.go`；`relay_transport_test.go` |
| B35b | ✅ 已实现 | Transport | **修复：** 桥自身 HTTP/WS 路径原先只接受环回监听，且 Host/Origin 与监听地址逐字比较、`scheme` 只看 `r.TLS`——反代终止 TLS 时恒为 http，https 页面必被 403。现由 `--public-origin` 显式声明对外来源：非环回监听仅在该开关下放行且只接受私有/overlay 网段；Host/Origin 额外接受该来源；Cookie `Secure` 跟随其 scheme；WebSocket origin 白名单与同一规则对齐。不信任任何 `X-Forwarded-*`（S09）。 | `internal/transport/public_origin.go`、`cmd/pi-bridge/main.go`；`public_origin_test.go` |
| B36 | ✅ 已修 | Bridge | 本轮修复：扩展状态快照从 transport 的**全局 byKey** 移到 runtime 的 worker 上（`extensionState` 按 worker 持有），更新点从「浏览器订阅转发协程」移到 **Pi 事件消费侧**——没有浏览器连接时也记录，切会话不再串。快照随 epoch 重置（`identity.go` 重绑定清空），新增 `session.ext_status` 带回 epoch，前端订阅确认后补齐且**只补本地缺的 key**（避免快照倒退）。反例：`Test扩展状态快照不依赖订阅者`、`Test扩展状态按worker隔离`、`Test扩展状态快照有界且换epoch清空`，transport 端到端 `Test扩展状态快照不依赖订阅且按会话取`；前端 `订阅确认后补齐扩展状态行`、`快照不覆盖已收到的新状态`。 | `internal/transport/extension_state.go`；`internal/transport/server.go`；`pi-web/hooks/useAgentSession.ts` |
| B37 | ✅ 已修 | Bridge | 本轮修复：标题改从文件两端取——头部窗口读第一条用户文本，尾部反向按块找最新 `session_info`（块间重叠 4 KiB，最多 64 块）。反例：`title_test.go` 两条——计数 ReaderAt 证明 8 MiB 文件只读 < 1 MiB（旧实现扫全文）；8 种偏移覆盖跨块边界。末尾半行沿用「未写完不算」语义。 | `internal/sessions/title.go`；`title_test.go` |
| B38 | ✅ 已修 | Bridge | 本轮修复（与 O03 合并）：`rawEntry` 走 `scanNodes`（与 History 同一缓存键），命中后按 offset/size ReadAt 单条并核对记录 ID；冷路径全扫一次写缓存，校验失败回退线性扫描。**语义变化**：坏文件（如缺 `parentId`）现在与历史页同样报 `invalid_history`，不再被宽松跳过——三处测试夹具因此按真实 Pi 格式修正。反例：`Test惰性读取复用扫描索引`（旧实现 `scan.stats()` 为 0）。 | `internal/sessions/lazy.go`；`store.go`；`lazy_test.go` |
| B39 | ✅ 已修 | Bridge | **修复：** 压缩命中先返回缓存，未命中才读原文；回归用「预热后删除原文件仍可命中」验证。原问题：命中前仍 `os.ReadFile` 并分配完整 JS/CSS。 | `internal/presentation/presentation.go`；`compress_test.go` |
| B40 | ⚠️ 部分（部署边界） | Runtime | 本轮补齐桥可控的一半，两侧都有反例。**Pi 侧**：`Test停止回收同组后代`（fake Pi 起同组 `sleep`，`Stop(true)` 后后代必须消失；改成只杀直接子进程即失败）。**终端侧发现真实缺口**：pty 以 `Setsid` 起 shell，交互 shell 的 job control 会把后台作业放进**它自己的进程组**——实测 `sleep 300 &` 得到 `shell: pgid=pid,sid=pid` 与 `job: pgid=自身,sid=shell`，所以 `killGroup(-shellPid)` 必然漏掉作业（诊断：Close 后 `shell=false, job=true`）。已加 `killSession`（扫 `/proc` 按 sid 清点，`sessionOf` 从最后一个 `)` 起解析，避开 comm 含空格/括号的陷阱），关闭路径改为「组 + 会话」两步；反例 `Test关闭终端回收同组后代` 与 `Test会话成员含同会话后台作业`。**桥被 SIGKILL** 后的回收不在桥内可实现——`Pdeathsig` 只覆盖直接子进程，必须由服务管理器按 cgroup 监督：README 已给出 `KillMode=control-group` 的 unit 片段。 | `internal/runtime/process_linux.go`；`internal/terminal/proc_lin.go`；`process_group_test.go`；`shell_test.go`；README |
| B41 | ✅ 已修 | Relay | **修复：** `Close()` 遍历 `s.clients` 一并 cancel 并关闭，浏览器不再挂到对端超时。回归断言必须读到「连接已关闭」而非读超时。 | `internal/relay/server.go`；`relay_limits_test.go` |
| B42 | ✅ 已修 | Bridge | **修复：** 状态设 64 KiB 原始字节及条目双限，预留 JSON/WS 空间，返回完整记录及 truncated，UI 显示截断。精确边界及转义预算回归通过。原问题：`GitStatus` 的 2 MiB stdout 截断被 `gitOutput` 丢弃；10000 个未跟踪文件的探针只返回 9119 条且无 `truncated` 字段，接口静默显示不完整状态。超过 512 KiB 的列表还会超出 WS 响应帧上限。 | `internal/workspace/git.go`；`git-status-limit-probe.log` |
| B43 | ✅ 已修 | Bridge | 本轮修复：① 跳过的超大文件也计入访问预算（`scanned++`），否则装满大文件的目录会被逐个 stat 到底；② `searchFile` 接 ctx 并每 64 行检查一次取消；③ 打开句柄后按真实尺寸再判一次 `MaxFileBytes`（目录项尺寸与读时可能不同）。反例：`search_test.go` 两条——稀疏大文件目录的 `scanned=5`、12 MiB 单文件在 5ms 后取消必须返回错误。 | `internal/sessions/search.go`；`search_test.go` |
| B44 | ✅ 已修 | Build | **修复：** 终端平台差异收敛到 `proc_lin.go`/`proc_oth.go`，非 Linux 显式报错。linux/darwin/windows 三平台 `go build` 与 `go vet` 均通过。 | `internal/terminal/{terminal,proc_lin,proc_oth}.go` |
| B45 | ✅ 已修 | Bridge | 本轮与 B76/B77 一起做：新增 `sessions.ExportDocument` 从 **JSONL 磁盘投影**导出，**不启动 worker**、不依赖 Pi 的 `export_html`。投影是**迭代**实现（`exportRows`/`assistantRows`逐条摊平），深链不会递归爆栈——`Test导出深链不递归` 钉住；超长内容按行截断（`Test导出按行截断超长内容`）。 | `internal/runtime/session_ops.go`；`pi-web/app/api/sessions/[id]/export/route.ts` |
| B46 | ✅ 已修 | Relay | **修复：** Cookie 属主改 base64url 编码，彻底消除 '.' 分隔符冲突；同时限定 owner/deviceId 字符集（拒绝控制字符与空白，允许 '.'）。 | `internal/relay/users.go`；`users_persist_test.go` |
| T01 | ✅ 已修 | Tests | 假 Pi 改为每个测试进程独占临时目录；sync.Once 仅复用进程内产物，runtime/transport/testutil 在 TestMain 统一清理。并发构建及编译失败清理测试通过，恢复旧固定路径后反例按预期失败。 | `internal/testutil/fakepi.go`；三个包的 `main_test.go`/`TestMain`；P0 |
| U06 | ✅ 已修 | UI | 文件列表先按每个xhr的目标/会话代次在beforeOnLoad拒绝旧响应，再保留路径守卫；本地目录意图也有revision。 | `src/modules/fragment-requests.ts`、`workspace.ts` |
| U07 | ✅ 已修 | UI | **修复：** 惰性图片在 `load` 后 `revokeObjectURL`，不再把 blob 留到页面卸载。 | `src/modules/lazy.ts` |
| U08 | ✅ 已修 | UI | **修复：** `flatten` 改为显式栈迭代（栈内带 depth），前序结果不变；20 万层线性树回归稳定复现原栈溢出。 | `src/modules/branch.ts`；`tests/unit/branch.test.ts` |
| U09 | ✅ 已修 | UI | **修复：** `refresh()` 即使在途补全请求失效，debounce 窗口内的旧候选不再写入新菜单。 | `src/modules/mention.ts`；`tests/unit/mention.test.ts` |
| U10 | ✅ 已修 | UI | 每个xhr登记发起epoch，A→B→A不再仅比较URL与最新pendingHistory；切会话取消在途读取，beforeOnLoad拦截旧响应。 | `src/modules/fragment-requests.ts`；`tests/unit/fragment_requests.test.ts` |
| U11 | ✅ 已修 | UI | **修复：** 排队模式回读改看 `followUpMode`（选 followUp 时桥改的是这个字段），缺失时不猜、保持当前选择。 | `src/modules/workbench.ts`；`tests/unit/workbench.test.ts` |
| U12 | ✅ 已修 | UI | **修复：** 关闭文件预览改用 `elOrNull`，图片预览顶掉 `#file-content` 后不再抛异常。 | `src/modules/workspace.ts` |
| U13 | ✅ 已修 | UI | **修复：** `send`/`sendQueued` 的目标会话在发起时取定；切换后明确报错并保留输入与附件，不静默丢弃。 | `src/modules/workbench.ts`；`tests/unit/workbench.test.ts` |
| U14 | ✅ 已修 | UI | **修复：** 自动重试改为按会话记忆的本地偏好（`autoRetryBySession`），切换会话时套用该会话上次选择、默认关闭；模板明确标注「不是 Pi 的实时状态」。 | `src/modules/workbench.ts`；`src/templates/shell.html` |
| U15 | ✅ 已修 | UI | 本轮修复：三条「实时流不再可信」的路径合并到同一个 `resyncFromStream`——桥明确回 `resync_required`、订阅被关闭且带 `resyncRequired`、事件因体积被省略（`bridge.event_omitted`）。它只做**只读重建**（重读历史、重订、刷新扩展对话），绝不重发有副作用的命令（#258）。 | `pi-webui-htmx/src/modules/workbench.ts`；`pi-bridge-go/internal/runtime/manager.go` |
| U16 | ✅ 已修 | UI | 本轮修复：订阅确认帧此前没有守卫。现在 `session.subscribe` 在**发起时**固定 scope（`scope.current$()`），确认回来后先 `scope.alive()` 校验才 `cursor.begin(...)`——等待期间切了会话，旧会话的 ACK 不得改写新视图的游标。切 epoch 仍然只发生在确认帧处。 | `src/modules/stream.ts`、`src/modules/workbench.ts`；`internal/transport/server.go:subscribeWithReplay` |
| U17 | ✅ 已修 | UI | xhr→epoch的本地映射在beforeOnLoad核对，先于HX响应头/OOB；不需要让服务器回显自定义头。A→B→A反例覆盖，动态思考按钮也归属会话。 | `src/modules/fragment-requests.ts` |
| U18 | ✅ 已修 | UI | 本轮修复：`refreshDialogs` 已有单飞 + dirty 循环，补齐了**视图代次**——快照请求前后各取一次 scope，`alive()` 不成立就丢弃结果；`answerDialog` 在回复前固定 scope，迟到的成功与错误都不改动新会话的对话框、状态行或提示。 | `src/modules/workbench.ts` |
| U19 | ✅ 再修复 | UI | 模型表单重做后曾回归（F01）。现在维护单一草稿，保存独立快照、完成不重读，重复保存合并；选节点/JSON/末项删除也不丢字段。不承诺跨浏览器配置CAS。 | `src/modules/models.ts`、`tests/unit/models_draft.test.ts` |
| U20 | ✅ 已修 | UI | **修复：** 附件批次改串行队列，每一批都在上一批落地后判断额度，并发批次不再突破 8 张上限。 | `src/modules/workbench.ts`；`tests/unit/attachments_scope.test.ts` |
| U21 | ✅ 已修 | UI | **修复：** 思考增量改为追加文本节点（O(增量)），不再每 token 重写整段 40K 文本；上限用独立计数器，不读 DOM。 | `src/modules/stream.ts`；`tests/unit/liveview.test.ts` |

## 补充发现（源码核对/定向复现）

| ID | 严重性 | 项目 | 问题与影响 | 主要位置 |
|---|---|---|---|---|
| B47 | ✅ 已修 | Storage | **修复：** 轮转文件按序号升序载入（当前 → .1 → .2 → .3），同一 requestId 只保留更晚结论。回归直接构造「新记录在 .1、旧记录在 .2」的布局。 | `internal/storage/receipts.go`；`receipts_test.go` |
| B48 | ✅ 已修 | Runtime | **修复：** 记录每个对话的登记时间，回收协程里清理超过自身 `timeout` 的对话并通知前端；无 timeout 字段的对话不误清。回归验证清理后 `busyLocked()` 为假、空闲回收恢复。 | `internal/runtime/dialogs.go`；`manager.go`；`dialogs_test.go` |
| B49 | ✅ 已修 | Management | 模型摘要按 Pi 数组计数并对总输出应用限额，稳定排序 provider，保留原始 modelCount 并标记截断；旧对象夹具已改为真实数组。 | `internal/management/config.go`；`config_safety_test.go` |
| B50 | ✅ 已修 | Runtime | 本轮修复：`stop` 分级停止改成步骤表，总预算 = 步骤数 × StopGrace 由结构保证；`closing` 之后再次 `Stop` 不再无条件等 `done`，改为等到该预算的终点（`awaitStopped`）。反例 `runtime/stop_test.go` 两条：旧实现下第二条 Stop 阻塞满 3 秒判定超时。 | `internal/runtime/manager.go` |
| B51 | ✅ 已修 | Management/Workspace | 本轮修完：`Files.Read`/`Files.Image` 改为「打开句柄 → 句柄上 stat → 按上限截断读取」（`readAtMost` 用 LimitReader，最多读 limit+1 字节）。反例：`files_test.go` 的两条——超一个字节拒绝、恰好等于上限成功；计数 reader 证明最多读 65 字节（旧实现读了 1048576）。配置侧的实际 reader 限制不变。 | `internal/workspace/files.go`；`files_test.go` |
| B52 | ✅ 已修 | Sessions | **修复：** `walkDir`/`computeFingerprint` 接收 context（入口与内层双检），深度上限 32；空目录不计入文件上限。 | `internal/sessions/index.go`；`index_test.go` |
| B53 | ✅ 已修（资源边界；分片经评估不做） | Relay | 兼容性面（帧上限统一、封装不做 HTML 转义）此前已修。本轮补**资源边界**：`outbound` 取代裸 channel+计数器——① 队列预算固定 4 MiB，不再随单帧放宽；② 超过预算的单帧要求**独占队列**（占用 = 它自己）；③ 超预算时**等预算归还**（最多 2 秒）而不是立刻断连；④ 小帧（≤ 8 KiB，只看长度不看内容，保持 routing-only）走独立高优先通道，`pumpWrites` 优先排空它。反例：`Test队列放行超过预算的单帧`、`Test超预算先等归还再放弃`、`Test记账归还后归零`、端到端 `Test满预算时控制帧等待而不是断连`（16 MiB 写入 + 慢消费者；旧放行线下连接直接断开）。**仍未做**：隧道分片/credit——它是线上协议加宽，必须与 B54 的 HTTP 大内容一起定；在那之前单帧仍可达 `maxFrame`，单连接占用上界是「一个在写帧 + 预算」。 | `internal/relay/outbound.go`；`frame_limit_test.go`；`priority_e2e_test.go` |
| B54 | ✅ 已修（通道 + 前端路径均落地并端到端实测） | Relay/UI | 分两步完成。**通道**：relay 新增设备前缀 `GET/POST … /d/{deviceId}/{路径}`——用户认证 + 设备归属校验 + 请求封装成 HTTP 帧经隧道发给设备，设备在**桥自己的 HTTP handler** 上跑一遍再回帧，响应按 ID 配对；`/d/{id}` 308 规范化到 `/d/{id}/`；设备前缀下的 WS 别名复用 `/client` 的认证与连接上限。桥侧显式把 Host 设成桥自己的、Origin 由 relay 剥离，**桥的来源校验原样保留**（实测伪造 Origin 被拒）。**前端路径**：模板与外壳 URL 全部相对化 + 桥渲染 `<base href>`（`DocBase` 只接受纯路径，拒绝协议、主机、`..`、双斜杠）；前端 fetch / htmx / WS / pushState 相对化；Vite 改用相对 base。**实测端到端**：本地 relay + 真隧道 + 桥，取到 33 KB 外壳、WS 打开、状态「已连接」、会话列表加载，relay 重启后自动重连。分片经评估不做（四层上限自洽，见 remaining-issues-plan.md）。 | `internal/relay/http_proxy.go`；`internal/transport/tunnel_http.go`；`src/lib/url.ts`；`http_proxy_test.go`；`tunnel_http_test.go`；`tunnel_e2e_test.go` |
| B55 | ✅ 已修 | Relay | 默认目录改为用户持久位置：`$XDG_STATE_HOME/pi-relay` → `~/.local/state/pi-relay`（旧默认是 `os.TempDir()/pi-relay`，重启或清理 `/tmp` 后设备注册表整体消失）。显式把 `--state-dir` 指到临时目录时启动打 WARN，不静默接受。反例：`cmd/pi-relay` 的 `Test默认状态目录是持久位置`（回到旧默认即失败）。 | `cmd/pi-relay/main.go`；`main_test.go` |
| B56 | ✅ 已修 | Workspace | **修复：** stderr 限 32 KiB、不回显；取消进程组的真实后代测试通过。桥 SIGKILL 的整树保证仍属 B40/P7。原问题：`gitOutputLimited` 仅限制 stdout，stderr 使用无界 `bytes.Buffer`；同时 `CommandContext` 只回收 Git 直接子进程，仓库配置触发的外部命令后代可能存活。 | `internal/workspace/git.go` |
| B57 | ✅ 已修 | Tunnel | **修复：** pump 发送失败即标记 dead 并从映射摘除，同时释放订阅与终端；`acquire` 遇到死连接会替换。回归验证重连后拿到新连接且能收到响应。 | `internal/transport/tunnel.go`；`tunnel_test.go` |
| B58 | ✅ 已修 | Storage | **修复：** 打开追加句柄前把日志截回最后一个完整换行。回归模拟崩溃半行后追加并重开，校验每一行均可解析。 | `internal/storage/receipts.go`；`receipts_test.go` |
| B59 | ✅ 已修 | Management | 复核（2026-09-30）：`npmPackageName` 已剥离 `@version` 后缀（含 `@scope/pkg@ver` 的 scope 处理），传回纯包名用于 registry 查询与已安装版本读取。 | `internal/management/packages.go`；`pinned-package-probe.log` |
| B60 | ✅ 已修 | HTTP | **修复：** 解析 qvalue，省略视为 1，未列出且无 `*` 视为不可接受，同名重复取最严格，非法 q 视为禁用。回归覆盖 `*`、`*;q=0`、`br;q=0`、同名重复与畸形 q。原问题：忽略 qvalue，`br;q=0` 仍选 br。 | `internal/presentation/presentation.go`；`compress_test.go` |
| B61 | ✅ 已修 | HTTP | **修复：** `Vary` 无条件声明；端到端测试按 8 种 Accept-Encoding 校验 Vary、Content-Encoding 与解压结果。原问题：仅压缩分支设置，identity 缺 Vary。 | `internal/transport/server.go`；`server_test.go` |
| B62 | ✅ 已修 | Sessions | **修复：** trash 存在却执行失败时报错取消，绝不退回 `os.Remove`；只有系统确实没有 trash 才真正删除。两条回归分别覆盖失败与缺失路径。 | `internal/sessions/delete.go`；`delete_test.go` |
| B63 | ✅ 已修 | Relay | `--host` 从「可选、为空即整体跳过校验」改为**必填**：`relay.Config.Validate()` 在构造期拒绝空值（含纯空白）与带协议/路径的值，`NewServer` 因此返回 `(*Server, error)`；`ServeHTTP` 的两道校验不再有「跳过」分支，Origin 改成解析后按 scheme + host 比对。匹配语义：声明**含端口**则精确比对，不含端口则忽略请求端口（端口是部署细节，且不影响这套校验要挡的 DNS rebinding）；IPv6 要写成 `[::1]:port`。反例：`host_guard_test.go` 三条——空/空白 host 构造失败、域名不匹配与跨源 Origin 一律 403、带端口声明按精确比对（把校验改回 `s.host != "" && …` 即失败）。 | `internal/relay/server.go`；`cmd/pi-relay/main.go`；`host_guard_test.go` |
| B64 | ✅ 已修 | Runtime | 复核（2026-09-30）：`CheckRebindTarget` 已成为独立步骤，由 `internal/runtime/identity.go` 在身份切换**之前**调用，冲突时 Pi 还没切走。 | `internal/runtime/identity.go`；`internal/runtime/manager.go` |
| B65 | ✅ 已修 | Events | **修复：** `resetReplay` 同时更换 epoch；旧 epoch 一律拒绝并强制重新同步，不再用「返回空」假装已同步。new/switch/fork/clone 四条路径都经 `Rebind`，覆盖完整。反例（只归零 seq）验证通过。 | `internal/runtime/identity.go`；`identity_test.go` |
| B66 | ✅ 已修 | Runtime/UI | 本轮修复：`protocol.Spec` 增加 `Timeout`，`TimeoutFor` 由 `claims.go` 取用。压缩/用户 bash 5 分钟、导出 2 分钟、搜索 1 分钟、网络查询 45–90 秒；前端 `workbench.ts` 的等待上限同步，未标注的方法仍按默认超时。反例：`-run Test长任务命令不被默认超时砍断`（同延迟下 compact 成功、session.stats 超时）。 | `internal/transport/server.go`；`internal/runtime/manager.go`；`pi-webui-htmx/src/modules/bridge.ts` |
| B67 | ✅ 已修 | Runtime | **修复：** 对话登记移到体积上限检查之前，超大对话也占住记录并可回复；超出 `MaxDialogs` 时明确取消并推送说明。 | `internal/runtime/manager.go`；`dialogs_test.go` |
| B68 | ✅ 已修 | Runtime/Security | **修复：** Pi/PTY/Git 共用服务环境过滤；真实 spawn/PTY 测试和反向验证通过。保留正常 API、代理及 Pi 环境，不等同同 UID 的 OS 隔离。原问题：Pi 与 PTY 子进程直接继承桥的完整 `os.Environ()`，包括 `PI_BRIDGE_TOKEN`、`PI_BRIDGE_DEVICE_TOKEN`；agent bash、项目扩展或终端命令可读出桥/设备凭据。 | `internal/runtime/manager.go`；`internal/terminal/terminal.go` |
| B69 | ✅ 已修 | Management/Security | 写入使用随机独占 0600 临时文件、文件 Sync、rename 和目录 Sync；固定路径 symlink 不再被触碰。同步不明返回 outcome_unknown，临时文件统一清理。 | `internal/management/config.go`；`config_safety_test.go` |
| R02 | ✅ 已修 | UI | 本轮视觉核对时量出来的两处样式静默失效：① `--font-mono` 被 app.css 六处 `var()` 引用却从未定义，工具块/思考块/配置编辑器实际继承正文字体（界面上看不出错，只是与 Pi Web 不同）——已在 tokens.css 定义并加契约检查「引用的自定义属性必须有定义」；② `.tool-call` 有两条同特异性规则，后一条的 `font-size:12px` 压过 B 批对齐的 11px——已删重复声明并加契约检查锁住字号只声明一次。 | `src/styles/tokens.css`；`src/styles/app.css`；`scripts/check-contract.mjs` |
| B70 | ✅ 已修 | Product | 本轮补上目录接线：新增 `/ui/models/catalog` 片段端点（`config.catalog` 的 HTML 出口）——候选由桥渲染成可点击行并带 `data-catalog-*`（型号/显示名/上下文/最大输出/推理/图片），前端只做「点击 → 填表」，且**只填空字段**、不覆盖用户已写值。目录来源改为可注入（`management.Config.SetCatalogURL`，只由部署者与测试设置，不接受网页输入），进程内缓存 10 分钟、单次返回上限 50 条、支持按 ID/名称搜索。旧的 provider/model 字段编辑器、思考三态与本地草稿保持不变。 | `internal/transport/model_catalog.go`；`internal/presentation/model_discovery.go`；`src/templates/catalog.html`；`src/modules/models.ts` |
| B71 | 🚫 部署约束 | Sessions | 已声明的部署限制，不是待修缺陷：桥内单 writer 不能约束另一桥或不合作的外部 Pi CLI。保持独立会话目录；合作锁只约束参与者。本轮共用扫描索引（O03/O04）沿用同一前提：**不假设桥是唯一写者**，因此按 size/mtime/dev-ino 校验、读出的记录还要核对 ID（B38）。 | `internal/runtime/manager.go`；`internal/sessions/store.go`；架构 S03 |
| B72 | ✅ 已修 | Sessions | 本轮修复：`sessions.MaxMatchesLimit=500` 在 `Store.Search` 入口夹紧，客户端只能收紧或放宽到该上限；反例 `-run TestSearch命中数上限被夹紧` 在旧实现下返回 600 条。 | `internal/transport/server.go`；`internal/sessions/search.go`；`search-limit-probe.log` |
| B73 | ✅ 已修 | Tunnel | 本轮修复，且**原描述不准**：`subs` 早已由 `bridge.mu` 保护并被 `subscribe/unsubscribe` 串行化；真正无锁的是 `terms`（`terminal.open/close` 走并行分发），`-race` 并发终端可复现。同一段代码还有第二处缺陷：`acquire` 在持有 `t.mu` 时调用会再次加锁的 `releaseAll`，命中「dead 已置位、尚未从映射摘除」的窗口会把整个 TunnelBridge 永久锁死（实测卡死超时）。修法：`subs`/`terms` 统一到 `bridge.mu`，「摘取句柄」与「关闭句柄」拆成两步，`acquire` 改为显式解锁。反例：`tunnel_concurrency_test.go` 两条。 | `internal/transport/tunnel.go`；`tunnel-map-race-probe.log` |
| B74 | ✅ 已修 | Tunnel | 复核（2026-09-30）：`handle` 已按连接持有 `release` 信号量并在满时回 `busy`，且非 urgent 命令同样占用 `Server.operations`。 | `internal/transport/tunnel.go` |
| B75 | ✅ 已修 | Terminal | 本轮修复：`resolveShell` 改为**本机固定受信路径表**（名字 → `/bin`、`/usr/bin` 下的规范文件），不再 `exec.LookPath`，也不再接受表外的绝对路径；调用方给的绝对路径必须与表中候选是同一文件（`os.SameFile`，`/bin`→`/usr/bin` 的 symlink 仍可用）。反例：`shell_test.go`——PATH 上的伪装 bash 与表外绝对路径都被拒绝，旧实现第一步即失败。 | `internal/terminal/terminal.go`；`shell_test.go` |
| B76 | ✅ 已修 | Export | 本轮与 B45/B77 一起做：`session.export_html` 改为走 `s.store.ExportDocument`（磁盘投影），**不再经过 `ensureWorker`**——导出一份会话不再需要拉起 `pi --mode rpc` 子进程，也不再有「打开导出会启动 worker」的副作用。 | `pi-webui-htmx/src/modules/workbench.ts`；`pi-bridge-go/internal/transport/server.go`；`pi-web/app/api/sessions/[id]/export/route.ts` |
| B77 | ✅ 已修 | Export/Storage | 本轮与 B45/B76 一起做：配额从「写入前裁一次」改为完整边界——① 整个「裁剪 → 投影 → 渲染 → 写入」持 `exportMu`，并发导出不再各自看到同一个空位而一起写进去；② 裁剪时按 `maxExportFiles-1` 预留新产物的位置；③ 单文件上限（`maxExportFileBytes`）与目录总量上限（`maxExportBytes` = 256 MiB / 32 个）双重约束。反例：`Test导出目录按数量裁掉最旧的`、`Test导出目录按字节裁掉最旧的`、`Test反复导出不会撑爆导出目录`。 | `internal/transport/export_store.go`；`internal/transport/dispatch.go:session.export_html` |
| B78 | ✅ 已修 | Relay | 本轮修复：`persist` 改为落盘成功之后才清 `dirty`，并用 `version` 自增判断「写盘期间是否又有新改动」，有新改动就留给下一次。反例 `-run Test注册表写盘失败后不清脏`：故障解除后不再产生新改动，只重跑写盘，旧实现直接返回 nil 导致设备不落盘。 | `internal/relay/registry.go`；`registry-persist-probe.log` |
| B79 | ✅ 已修 | Terminal | 本轮修复：`Terminal` 记下 `maxCols/maxRows`，`Resize` 超限夹到上限（而不是像 `Open` 那样回落默认值——窗口尺寸是真实值，静默改成 80×24 会让终端突然缩小）。反例 `-run Test终端尺寸上限对resize同样生效` 在旧实现下读到 65535。 | `internal/terminal/terminal.go`；`internal/transport/server.go` |
| B80 | ✅ 已修 | Protocol | 本轮修复：发现端点移除写死的 `phase`（阶段早已完成），终端数量与空闲秒数改报 `terminal.Manager.Limits()` 的真实配置。反例 `transport/capabilities_test.go` 用非默认终端配置断言报的是真实值。 | `internal/transport/server.go`；`cmd/pi-bridge/main.go` |
| B81 | ✅ 已修 | Management | 网页新增/修改 apiKey/header 命令表达式被拒绝，只允许按原身份保留本机已有值；Pi 的 `$!` 字面量转义不误判。Go 回归验证拒绝和保留，没有实际执行凭据命令。 | `internal/management/config_values.go`；`config_safety_test.go`；Pi `resolve-config-value.js` |
| D01 | ✅ 已修 | Docs | 复核（2026-09-30，J 批次收尾）：README 的阶段表述已按验收项改写（不再整阶段打勾）；前端 README 的实测数字重新校准为当前值（首屏自有 28.73 KiB / 30 KiB、总计 46.32 KiB / 50 KiB、22 个测试文件 178 项通过）；`code-audit.md` 的状态列按本轮实际修复逐项改写。 | 两个项目的 README/docs、protocol 文档 |
| D02 | ✅ 已配置 | Tests | 两仓独立 GitHub Actions 工作流已配置并固定 action SHA；本地等价命令及 YAML 结构检查通过。`scripts/verify-pair.sh` 强制真实 UI checkout 做跨仓联测；当前没有 Git remote，托管运行待首次接入，不声称已经跑绿。其他审查反例随修复逐项转为正式测试。 | 两仓 `.github/workflows/check.yml`；`scripts/verify-pair.sh`；P0 |
## 限额与部署链核对

| 路径 | 前一层限制 | 后一层限制 | 结论 |
|---|---:|---:|---|
| 图片输入 | UI 单图 8 MiB、最多 8 张 | 浏览器 WS 请求帧 1 MiB，Go WS request 1 MiB，Pi RPC 命令帧也有大小限制 | 配置允许值与实际传输不兼容；base64 与缩略图还会先占用浏览器内存 |
| workspace 图片/文件响应 | `files.image` 最多 4 MiB；`files.read` 允许大文本 | WS 响应帧 512 KiB | 大文件读取/图片会在结果发送时取消连接；接口需分页、HTTP 二进制或统一限额 |
| bash 完整输出 | `session.bash_output` 单次最多 8 MiB | WS 响应帧 512 KiB | API 参数上限高于可传输上限 |
| Pi RPC / 会话树 | 桥的 RPC 单帧默认 8 MiB | `session.tree` 完整返回、不分页；超限关闭 bridge RPC client | 约 9 MiB 端到端失败不能证明上游限制；目标是磁盘分页树，保留有界 RPC，不无限放大缓冲 |
| Relay 隧道 | 本地帧最多 1 MiB | relay 再封装 `to`/`from` 路由 JSON，读帧仍限 1 MiB | 接近本地上限的帧因 envelope 膨胀被拒绝 |
| 会话搜索 | 每文件最多 16 MiB、最多扫描 200 文件 | `limit` 未封顶且取消仅在文件之间检查 | 用户请求可扩展结果切片；大单文件搜索期间不能及时取消 |
| 导出与包版本检查 | 单项有局部限制 | 导出目录无总量清理；插件配置无条数/并发上限 | 长期运行下磁盘和短时 goroutine/请求数没有总资源预算 |

## 与 Pi Web 的功能覆盖

| 状态 | 功能范围 | 审查结论 |
|---|---|---|
| ✅ 已有且主要链路已接线 | 会话列表/历史分页、流式对话、停止/排队、模型切换、分支操作、文件/Git、PTY、附件、扩展 UI、配置读取与写入 | 功能存在不等于边界正确；对应的大小、并发、切会话和错误恢复缺陷见上表 |
| ✅ 主要覆盖，个别简化 | 模型配置、models.dev 目录、历史分支树、文件预览、导出、设置、信任配置 | 字段表单 + 目录接线（B70）与磁盘树投影（U04）本轮补齐；导出改为只读磁盘投影、不再需要 worker（B45/B76/B77）；仍简化的部分：PDF 等富文档预览缺失、部分只读配置没有 UI |
| ✅ 已接通 | 云页面 → relay → 本地桥 | relay 设备前缀 `/d/{deviceId}/…` 同时承载 HTTP 与 WS：外壳、片段、资源与 `/api/v1/ws` 都经隧道到桥，前端全部用相对路径因此无需知道自己在哪种形态（B54，端到端实测） |
| 🚫 按用户决策不做 | OAuth 设备码登录、供应商额度查询、插件/技能远程安装/更新/搜索 | 用户明确只用 API 凭据、不查不可用额度，且远程安装插件等价于代码执行 |
| ⚠️ 暂缓/未立项 | Web Push 暂缓；PWA、版本检查/更新尚未立项 | 不把未实现推断成用户明确禁止 |
| ⚠️ 桥/UI 缺少或简化 | ChatMinimap、完整 PDF/文档查看、多语言、丰富斜杠命令面板、工作区 worktree | 参照 Pi Web 对应源码，按产品优先级决定，不能写成 Pi Web 也没有 |

## 审查完成度

两个仓库的生产 Go 模块、HTMX/TypeScript 入口和模块、模板、协议及主要资源生命周期已完成源码路径核对；主要跨层约束已与 Pi Web 对照。没有修改产品代码，所有问题保留为待修清单；每个 `高` 项修复时应把当前反例转成仓库回归测试。Pi Web 当前 checkout 的测试依赖不完整，因此对比结论来自源码而非该测试套件。

## 修复方案覆盖（2026-09-27，待实现）

完整设计、取舍与验收在 [architecture.md](architecture.md)，整体批次、依赖、代码影响及迁移以 [repair-plan.md](repair-plan.md) 的 P0–P7 为准。每个编号在下表有一个主要归属，共 105 个台账编号（B01–B81、U01–U21、T01、D01–D02），**不等于 105 个相互独立、全部运行复现的漏洞**；例如 B15/B74 是同一限流根因的不同表现。文档修订解决 D01 的描述漂移，但没有改变 B34/B80 的硬编码实现；D02 的独立 CI 工作流及显式跨仓联测入口已配置，本地验证通过，托管运行待首次接入远程；T01 已修并通过反向验证。U17的HTTP响应归属已在2026-09-30跨仓修复（UI `docs/htmx-css-ts-review.md` 第6节）；U18只补上同一HTTP守卫，不把pending集合的RPC快照乱序也宣称已完成。

| 方案 | 主要问题编号 | 决策与判定性验收 | 状态 |
|---|---|---|---|
| S01 共用 Executor / durable intent | B04、B15、B30、B31、B47、B58、B66、B74 | 先准入/claim/可靠 intent，再派发；并发同 ID、不同指纹、崩溃与轮转均不重复执行 | ✅ 已实现 |
| S02 原子 replay / Connection | B05、B14、B53、B57、B65、B73、U15、U16 | replay+订阅 fence；连接拥有取消资源；身份更换 epoch；双连接/重连/race 验证 | ✅ 已实现 |
| S03 身份事务 / 进程与删除 | B08、B09、B10、B40、B44、B50、B62、B64、B68、B71、B75、B79 | 先预留后切换，Stop 有界；删除收敛 writer；受监督 cgroup，明确外部 CLI 与同 UID 边界 | ⚠️ 桥侧已完成；B40 的服务管理器监督与 B71 的外部 writer 约束留给部署 |
| S04 配置 schema / 凭据 | B01、B02、B17、B32、B49、B51、B59、B69、B70、B81、U19 | revision/秘密操作/安全写；禁止重定向/新执行表达式；包并发上限；重排、并发保存和故障验证 | ✅ P1 基础秘密/安全写/出站；⚠️ revision、包、UI 等待实施 |
| S05 共用只读索引 / 扫描预算 | B06、B11、B12、B13、B27、B28、B29、B37、B38、B43、B52、B72、U04、U08 | 文件身份与完整行验证；标题/lazy/tree 复用；替换、50 MiB、深树、超多目录与取消测试 | ✅ 已实现 |
| S06 HTTP 大内容 / 导出 | B07、B33、B45、B76、B77、U05 | 小控制帧+有界资源；上传计算 Pi 编码；只读导出0 worker、临时配额与清理 | ✅ 已实现 |
| S07 UI SessionScope / 意图 | B03、U01、U02、U03、U06、U09、U10、U11、U12、U13、U14、U17、U18、U20、U21 | 目标发起时捕获、响应处理前守卫、并发预留；逐 await 切换、草稿/队列与 rAF 行为验证 | ✅ 已实现 |
| S08 扩展资源状态机 | B16、B36、B48、B67 | 分类/期限/写入回执；非法回复可重试，超大/过期/关闭不留幽灵 pending | ✅ B16/B48/B67 已修并回归；B36 待实施 |
| S09 relay 持久身份 / 部署 | B19、B20、B21、B22、B23、B24、B35、B41、B46、B55、B63、B78 | 持久与易失状态分离；TTL/连接预算/安全 Cookie/WSS；重启、写失败、撤销与 HTTPS 反代测试 | ✅ 已实现 |
| S10 受限 Git runner | B18、B25、B26、B42、B56 | 禁隐式 helper、流式预算、unborn 支持；标记脚本不执行、截断可见、后代收敛 | ✅ P1 实现与两仓联测 |
| S11 HTTP 缓存 / 内容资源 | B39、B60、B61、U07 | qvalue/identity/Vary 矩阵，先查缓存；反复挂载释放 URL/组件资源 | ✅ B39/B60/B61 已修并两仓联测；U07 待实现 |
| S12 云适配 / 能力与回归 | B34、B54、B80、D01、D02、T01 | 同源 HTTP+WS 云链路，方法/限额同源，版本协商，CI与隔离夹具 | ✅ 同源 HTTP+WS 云链路（B54）、方法/限额同源、版本协商与隔离夹具均已完成 |

实施顺序采用整体规划 P0–P7；S 编号仍作为领域归属，不再维护另一份粗粒度施工顺序。每批验证全部受影响入口/调用方与迁移，允许边界固定后的 UI/只读叶子模块并行，不允许复制执行器或以旧危险路径作为回退；台账只在专项验收通过后关闭。
