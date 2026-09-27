# 代码审查记录与修复方案索引

更新：2026-09-27。**P0 已完成，P1 正在实施，协议仍为 v1。** 下表 ✅ 表示对应代码与回归已完成，未标记项仍待修复。技术方案见 [architecture.md](architecture.md)，批次与进度见 [repair-plan.md](repair-plan.md)。

## 范围与基线

审查对象：`pi-bridge-go`、`pi-webui-htmx`；以 `/srv/projects/pi/pi-web` 的当前源码作对照。覆盖两个仓库的生产 Go 模块、HTMX/TypeScript 入口与模块、模板、协议和主要资源生命周期；对会话、凭据、命令与事件传输、工作区、Git、终端、relay、压缩和前端异步切换做了重点源码核对与定向反例。本文是源码审查记录，不是形式化证明；运行时不能安全或稳定触发的条目会明确标为源码确认。

审查基线：本轮开始时桥 `go test -race ./...`、`go vet ./...` 通过；前端 72 项 Vitest、TypeScript、生产构建及 `pnpm check` 通过。另有 16 个桥侧和 5 个前端初始审查反例未通过，后续定向探针继续新增发现；这些是审查探针结果，不是基线测试回归。探针存放在 `/srv/projects/agentTmp/pi-audit-vxcvho/`，未留在产品仓库。Pi Web checkout 未安装完整依赖；`npm test` 的 530 项中 115 项因缺少 `jiti`、`react`、Pi SDK 等依赖及平台相关路径用例失败，因此不将其测试结果当作产品代码质量结论。

## 代码审查发现

严重性：`高` 表示凭据/数据、重复副作用、主要交互或远程资源边界受影响；`中` 表示特定会话形状或恢复路径受影响。编号供后续修复与验证追踪。

| ID | 严重性 | 项目 | 问题与影响 | 主要位置 |
|---|---|---|---|---|
| B01 | ✅ 已修 | Bridge | 统一配置遍历器保护所有自定义头部值，并区分 provider/model 身份键与字段名；Raw/Models 共用脱敏。回归覆盖未知头名与特殊 provider 名。 | `internal/management/config_values.go`；`config_safety_test.go` |
| B02 | ✅ 已修 | Bridge | 秘密按 provider、模型 ID、override 键及大小写无关头部名恢复；重排不串值，不修改调用方对象。无来源/歧义占位符拒绝，明确新值可修复旧坏配置。 | `internal/management/config_values.go`；`config_identity_test.go` |
| B03 | ✅ 已修 | Bridge/UI | **修复：** 模板 radio 值、`queueKind()` 与 `refreshQueueState` 选择器统一为协议值 `steering`/`followUp`；桥对旧值 `steer` 给出可操作提示。回归同时锁定 wire 值与回读定位，两个反例均稳定失败。 | `pi-webui-htmx/src/{templates/shell.html,modules/workbench.ts}`；`internal/runtime/session_ops.go` |
| B04 | ✅ 已修 | Bridge | **修复：** 桥级 claim 注册表 + 命令指纹，本地 WS 与隧道虚拟连接共用同一 `admit`。64 并发压测验证只有一个放行；在途登记绝不被淘汰。原问题：`seen` 只在单连接内，两连接可同时执行同一 requestId。 | `internal/transport/claims.go`；`methods_test.go` |
| B05 | ✅ 已修 | Bridge | **修复：** `SubscribeWithReplay` 在单次持锁内完成「取快照 + 注册订阅」，并把 WS 与隧道两条入口的订阅逻辑收敛成一个共用实现。回归断言「快照末序号 == 注册序号」，反例（拆成两次加锁）5/5 稳定失败。 | `internal/runtime/manager.go`；`internal/transport/server.go`；`manager_test.go` |
| B06 | 高 | Bridge | 大 `get_tree` 的端到端请求后，后续状态返回 `worker_exited`。源码确认桥默认 `MaxFrame=8 MiB`，`Client.read()` 在超限时 fail 并关闭 stdin/stdout；原约 9 MiB 探针只能证明此链路失效，不能归因成 Pi 自身约 9 MiB 限制。 | `internal/runtime/manager.go:Defaults`；`internal/pi/client.go:read`；`internal/runtime/session_ops.go` |
| B07 | 高 | Bridge/UI | 桥接受 600 KiB 文件读取，但 JSON 响应超过 WS 帧上限并取消连接；常规文件预览会断线。 | `pi-bridge-go/internal/transport/server.go` |
| B08 | ✅ 已修 | Bridge | **修复：** 删除前先 `StopSession` 停掉该会话 worker，失败时非 force 明确拒绝、force 下仍尽力再停；结果带 `stoppedWorker`。原问题：worker 忙时也能删文件，Pi writer 仍存活。 | `internal/runtime/manager.go`；`internal/transport/server.go` |
| B09 | 中 | Bridge | `SwitchSession` 从 basename 直接解析 ID，不能识别 Pi 的 `timestamp_ID.jsonl` 标准命名。 | `pi-bridge-go/internal/runtime/identity.go` |
| B10 | 中 | Bridge | fork/clone 重绑定后停止的 Pi 进程仍可留在 manager 注册表。 | `pi-bridge-go/internal/runtime/manager.go`；`internal/runtime/identity.go` |
| B11 | 中 | Bridge/UI | 多个 assistant entry 的 thinking 占位符关联错 entry：较早思考块不可取回，后续块可能重复出现。 | `pi-bridge-go/internal/presentation/` |
| B12 | 中 | Bridge | scan cache 仅以 path/size/mtime 验证；同路径、同长度、保留 mtime 的原子替换可命中旧索引，叶子 ID 与实际记录不一致。 | `pi-bridge-go/internal/sessions/cache.go` |
| B13 | 中 | Bridge | 快速历史扫描可放过完整且已换行、但正文损坏的旧记录，与“完整损坏行显式报错”的契约不符。 | `pi-bridge-go/internal/sessions/scan.go` |
| B14 | ✅ 已修 | Tunnel | **修复：** subs 改为保存真实订阅句柄，退订与重复订阅都先 Close 旧订阅。回归做 12 轮订阅/退订后核对 worker 订阅数归零（反例下为 8，即上限）。 | `internal/transport/tunnel.go`；`tunnel_test.go` |
| B15 | ✅ 已修 | Tunnel | **修复：** 隧道命令与本地 WS 共用 `admit` 与全局 `operations` 预算。原问题：隧道路径完全绕过桥级并发上限。 | `internal/transport/tunnel.go`；`claims.go` |
| B16 | ✅ 已修 | Bridge | **修复：** 先校验参数再摘除对话；回执送达失败时把对话还回等待表。回归覆盖「非法回执后可合法重试」。 | `internal/runtime/dialogs.go`；`dialogs_test.go` |
| U01 | 高 | UI | 切换会话时 `selectSession()` 不清理全局附件数组；异步 `FileReader` 结果也没有会话 generation 归属，上一会话图片会留在或追加到新会话附件并可被发送。 | `pi-webui-htmx/src/modules/workbench.ts`；`src/modules/attachments.ts` |
| U02 | 中 | UI | 新会话首条消息前 `ensureWorker()` 刷新默认模型，覆盖用户已选择的模型。 | `pi-webui-htmx/src/modules/workbench.ts` |
| U03 | 高 | UI | `command()` await worker 启动后才读取当前 sessionId；等待期间切换会话会把原会话操作发给新会话。 | `pi-webui-htmx/src/modules/workbench.ts` |
| U04 | 中 | UI/Bridge | 无 worker 的历史会话打开分支面板时，`session.tree` 被桥拒绝；历史树浏览依赖显式启动会话。 | `pi-webui-htmx/src/modules/branch.ts`；`pi-bridge-go/internal/transport/server.go` |
| U05 | 高 | UI/Bridge | 附件控件允许 8 MiB 单图、最多 8 张并先读入 base64；桥的 WS 请求帧上限仅 1 MiB，稍大的单图就无法发送，前端还可能先持有数十 MiB 无法提交的附件。 | `pi-webui-htmx/src/modules/attachments.ts`；`pi-webui-htmx/src/modules/bridge.ts` |

## 继续审查发现（源码核对/定向复现）

以下不是上述 21 个反例的重复计数；来源为单独的本地探针或对具体执行路径的源码核对。尚需在最终报告中区分可重复的运行时复现与静态路径确认。

| ID | 严重性 | 项目 | 问题与影响 | 证据/主要位置 |
|---|---|---|---|---|
| B17 | ✅ 已修 | Bridge | 供应商请求使用拒绝重定向的独立 client；两个本地服务器的回归确认目标不接收凭据。并拒绝超限正文、禁止错误正文回显秘密。 | `management/discovery.go`；`discovery_safety_test.go` |
| B18 | ✅ 已修 | Bridge | **修复：** 统一 runner 清理环境、禁用已知 helper，转换过滤器明确拒绝；五类标记脚本回归通过。这不是任意 Git/恶意本机进程的 OS 沙箱。原问题：Git 查询执行仓库配置中的外部命令：`git status` 触发 `core.fsmonitor`，`git diff` 触发 `diff.external`。两个本地标记脚本探针均复现；Pi Web 的 status 路径也调用普通 `git status`，但它的 diff 明确带 `--no-ext-diff`。 | `workspace/git.go`；`pi-web/lib/git-changes.ts`；`git-probe.log`；`git-diff-probe.log` |
| B19 | 高 | Relay | `--add-user`/`--add-device` 在一次性 CLI 进程的内存 `Users` 表中添加后即退出；服务进程重建 `Users` 时表为空，故刚发出的用户 token 与设备预共享密钥无法认证。重启反例已复现。 | `cmd/pi-relay/main.go`；`internal/relay/users.go`；`relay-probes.log` |
| B20 | 高 | Relay | `ClaimTTL` 只作为 `expiresInSeconds` 返回，`Registry.Claim` 不检查配对码年龄；未被使用的配对码过期后仍可领取。 | `internal/relay/registry.go`；`internal/relay/server.go` |
| B21 | 中 | Relay | 配对尝试表按用户提交的 code 分桶；清理只删已过期项，没有活跃项硬上限。10000 个不同 code 的本地探针使 map 增长到 10000。 | `internal/relay/registry.go`；`relay-probes.log` |
| B22 | 高 | Relay | `persist()` 解锁后仍序列化 `[]*Device` 指向的共享对象；并发 `SetOnline` 会与 JSON marshal 读写同一字段。`go test -race` 定向压力探针报告数据竞争。 | `internal/relay/registry.go`；`relay-probes.log` |
| B23 | 中 | Relay | `/client` 可为同一 owner 的任意不同 `clientId` 建立 WS，`s.clients` 没有总连接数/每用户上限；公网 relay 可被认证用户用大量连接耗尽 goroutine 与内存。 | `internal/relay/server.go`；`cmd/pi-relay/main.go` |
| B24 | 高 | Tunnel | 设备长期 token 放在 `/tunnel?deviceId=...&token=...` 查询串，容易进入反向代理访问日志；桥也接受明文 `ws://` 到非环回 relay，token 会以明文出网。 | `internal/tunnel/client.go`；`internal/relay/server.go`；`cmd/pi-bridge/main.go` |
| B25 | ✅ 已修 | Bridge | **修复：** porcelain -z 同时读取分支与状态；空仓库、特殊文件名、重命名回归通过。原问题：合法的 unborn/空 Git 仓库没有 `HEAD`；`GitStatus` 先执行 `rev-parse --abbrev-ref HEAD` 并把失败作为整次查询失败。空仓库本地探针复现。 | `internal/workspace/git.go`；`git-probe.log` |
| B26 | ✅ 已修 | Bridge | **修复：** NUL 增量读取，2 MiB/50000 条上限，溢出取消整组，缓存传播 truncated；9000 长文件名回归通过。walk/大结果传输仍按 B27/S06 推进。原问题：`files.index` 的 Git 路径用 `cmd.Output()` 完整捕获 `git ls-files`，之后才应用 50000 条上限；超大仓库会先无界分配输出和 `strings.Split` 切片。 | `internal/workspace/index.go` |
| B27 | 中 | Bridge | `files.list` 与会话 `walkDir` 先 `ReadDir` 全目录再按上限截断；超大单目录可在限额检查前占用大量内存并排序。 | `internal/workspace/files.go`；`internal/sessions/index.go` |
| B28 | 中 | Bridge | 搜索遇到超长行会直接结束该文件扫描，但没有设置 `SearchResult.Truncated`；后续命中被跳过却报告结果完整。 | `internal/sessions/search.go` |
| B29 | 中 | Bridge | 会话索引 `fresh()` 在 TTL 内仍遍历整棵目录计算指纹；列表/历史查找在大目录下仍有 O(会话文件数) 开销。 | `internal/sessions/index.go` |
| B30 | ✅ 已修 | Bridge | **修复：** 有副作用命令派发前可靠写 pending intent（带指纹），完成后落终态；重启见到 pending 只回答 `outcome_unknown`，不假装成功。时序测试用会阻塞的假 sink 捕获「派发中已在盘上」。 | `internal/transport/claims.go`；`methods_test.go` |
| B31 | 中 | Bridge | 持久回执只按 `requestId` 查找，不校验重放请求的 method/sessionId；不同命令误用相同 ID 会收到旧命令的 `duplicate` 回执。 | `internal/transport/server.go`；`internal/storage/receipts.go` |
| B32 | 高 | Bridge | `config.packages` 对 settings 中每个 npm 包启动一个 goroutine/HTTP 请求；settings 文件有字节上限但没有 package 数或并发上限。 | `internal/management/packages.go` |
| B33 | 中 | Bridge | workspace 图片读取允许最多 4 MiB，`files.image` 却把图片 base64 放进 512 KiB WS 响应；大部分被桥识别为受支持的图片无法预览。 | `internal/workspace/files.go`；`internal/transport/server.go` |
| B34 | 中 | Bridge | `README.md` 仍标 A 阶段，并称 replay、持久去重、终端、Git、配置管理等未实现；能力端点也固定返回 `phase: A`，与实际实现及协议文档矛盾。 | `pi-bridge-go/README.md`；`internal/transport/server.go`；`api/v1/protocol.md` |
| B35 | 高 | Relay | TLS 反向代理以 HTTP 回源时，relay 从 `r.TLS` 推断 scheme 为 http，拒绝浏览器发送的 `https://relay-host` Origin；HTTPS 反代部署下客户端 WS 无法连接，且登录 cookie 不会设置 Secure。handler 探针已复现 403。 | `internal/relay/server.go`；`relay-origin-probe.log` |
| B36 | 中 | Bridge | `setStatus` 快照只按 key 全局存储，不含 sessionId；不同 Pi worker 的同名状态互相覆盖，切换会话可能看到另一会话的扩展状态。Pi Web 将状态保存在 per-session state。 | `internal/transport/extension_state.go`；`internal/transport/server.go`；`pi-web/hooks/useAgentSession.ts` |
| B37 | 中 | Bridge | 会话列表首次补标题时，`titleForPage` 为每条当前页会话从文件头扫描到尾，以找最新 `session_info`。多个长会话时列表请求重复读取大量完整 JSONL；这条路径不使用 History 的 scan cache。 | `internal/sessions/metadata.go`；`internal/sessions/index.go` |
| B38 | 中 | Bridge | 每次惰性加载 thinking/tool image 都由 `rawEntry` 从 JSONL 文件头逐行扫描到目标条目；History 建好的偏移索引/scan cache 未复用，展开多个旧块会重复扫描长会话。 | `internal/sessions/lazy.go`；`internal/sessions/cache.go` |
| B39 | ✅ 已修 | Bridge | **修复：** 压缩命中先返回缓存，未命中才读原文；回归用「预热后删除原文件仍可命中」验证。原问题：命中前仍 `os.ReadFile` 并分配完整 JS/CSS。 | `internal/presentation/presentation.go`；`compress_test.go` |
| B40 | 高 | Runtime | Linux `Pdeathsig=SIGTERM` 只作用于 Pi/terminal 的直接子进程，不会发给整个进程组；桥被 SIGKILL 后，忽略 SIGTERM 的 shell/扩展后代仍存活。带孙进程的 helper 反例已复现。 | `internal/runtime/process_linux.go`；`internal/terminal/terminal.go`；`pdeath-probe.log` |
| B41 | 中 | Relay | `Server.Close()` 注释称关闭全部连接，但只关闭 tunnels、不遍历 `s.clients`；活动浏览器 WS 在调用 `Close()` 后仍保持打开。定向 WS 测试已复现。 | `internal/relay/server.go`；`relay-close-probe.log` |
| B42 | ✅ 已修 | Bridge | **修复：** 状态设 64 KiB 原始字节及条目双限，预留 JSON/WS 空间，返回完整记录及 truncated，UI 显示截断。精确边界及转义预算回归通过。原问题：`GitStatus` 的 2 MiB stdout 截断被 `gitOutput` 丢弃；10000 个未跟踪文件的探针只返回 9119 条且无 `truncated` 字段，接口静默显示不完整状态。超过 512 KiB 的列表还会超出 WS 响应帧上限。 | `internal/workspace/git.go`；`git-status-limit-probe.log` |
| B43 | 中 | Bridge | 全文搜索最多遍历 200 个文件、单文件 16 MiB；超大文件会标截断但不计入 `scanned`，因此实际 I/O 可越过文件数预算；`ctx` 只在文件之间检查，单文件扫描期间取消不生效。并发搜索可放大磁盘与 CPU 消耗。 | `internal/sessions/search.go` |
| B44 | 中 | Build | `GOOS=darwin GOARCH=arm64 go test -exec=true ./...` 编译失败：`internal/terminal/terminal.go` 在通用文件直接使用 Linux-only `SysProcAttr.Pdeathsig`。这与 runtime `process_other.go` 声称非 Linux 显式报错的可构建路径不一致。 | `internal/terminal/terminal.go`；`cross-darwin.log` |
| B45 | 中 | Bridge | Pi Web 为 Pi 导出的 HTML 把 `sortChildren/mapNodes/markActive` 改为迭代实现，专门修复 5000+ 深树栈溢出；桥直接透传 Pi `export_html` 文件，没有同等处理，长线性会话导出后浏览器仍可能栈溢出。 | `internal/runtime/session_ops.go`；`pi-web/app/api/sessions/[id]/export/route.ts` |
| B46 | 中 | Relay | `AddUser` 不限制 owner 字符；用户名包含 `.` 时 `SignCookie` 产出的 `exp.owner.sig` 被 `CheckCookie(strings.Split(...))` 拆成多段，浏览器 cookie 永远认证失败。句点用户名探针已复现。 | `internal/relay/users.go`；`relay-owner-cookie-probe.log` |
| T01 | ✅ 已修 | Tests | 假 Pi 改为每个测试进程独占临时目录；sync.Once 仅复用进程内产物，runtime/transport/testutil 在 TestMain 统一清理。并发构建及编译失败清理测试通过，恢复旧固定路径后反例按预期失败。 | `internal/testutil/fakepi.go`；三个包的 `main_test.go`/`TestMain`；P0 |
| U06 | 中 | UI | 文件列表在 htmx swap **之后**才检查 generation；文本 `files.read` 等待后完全未检查。定向 Vitest 以延迟旧文件响应复现：新文件已展示后又被旧内容覆盖。 | `pi-webui-htmx/src/modules/workspace.ts`；`ui-workspace-file-probe.log` |
| U07 | 低 | UI | 惰性图片使用 `URL.createObjectURL`，替换/卸载图片时没有 `URL.revokeObjectURL`；长会话多次展开后 blob URL 保留至页面释放。 | `pi-webui-htmx/src/modules/lazy.ts` |
| U08 | 中 | UI | 分支树 `flatten()` 递归遍历深树；15000 层线性树的定向 Vitest 复现 `Maximum call stack size exceeded`，长会话分支面板失败。 | `pi-webui-htmx/src/modules/branch.ts`；`ui-more-probes.log` |
| U09 | 低 | UI | `@` 补全只在新请求开始时增加 seq；query 改变到下一次 debounce 触发之间，旧请求仍可能把旧候选写入新菜单。定向 Vitest 已复现。 | `pi-webui-htmx/src/modules/mention.ts`；`ui-more-probes.log` |
| U10 | 中 | UI | 分支树、fork 消息及 `gotoLeaf` 的历史片段没有完整的 session generation 守卫；切换会话后，旧树可写入新面板，旧分支历史也可能替换新会话的对话区。 | `pi-webui-htmx/src/modules/branch.ts`；`src/modules/workbench.ts` |
| U11 | 中 | UI | Pi 返回 `followUpMode: one-at-a-time` 时，前端没有映射到“完成后追加”选项，回读状态与实际队列模式不符。 | `pi-webui-htmx/src/modules/workbench.ts` |
| U12 | 低 | UI | 图片预览用 `<img>` 替换 `#file-content` 后，关闭操作仍对已不存在的节点调用 `replaceChildren()`，触发异常。 | `pi-webui-htmx/src/modules/workspace.ts`；`ui-more-probes-2.log` |
| U13 | 高 | UI | 发送先 await 模型设置；期间切换会话后，后续 `session.prompt` 读取新的 `sessionId`，把消息投给另一会话。 | `pi-webui-htmx/src/modules/workbench.ts`；`ui-send-race-probe.log` |
| U14 | 中 | UI | 自动重试没有 Pi 读回字段，但复选框是跨会话的单一 DOM 状态；切换会话仍显示上一会话最后一次手动设置，默认未勾也不代表当前 worker 实际状态。 | `pi-webui-htmx/src/modules/workbench.ts`；`src/templates/shell.html` |
| U15 | 中 | UI | `bridge.event_omitted` 控制事件被忽略；UI 不读取 `resyncRequired`，连接仍在线时不会立即重读历史，直到后续 settled/手动刷新。 | `pi-webui-htmx/src/modules/workbench.ts`；`pi-bridge-go/internal/runtime/manager.go` |
| U16 | 中 | UI | `EventCursor.accept` 接受任意新 epoch 并把 seq 重置；旧 worker 延迟帧可把 cursor 从新 epoch 切回旧 epoch，随后旧帧被当成新事件处理。 | `pi-webui-htmx/src/modules/stream.ts`；`pi-bridge-go/internal/transport/server.go` |
| U17 | 高 | UI | 精度校正（本轮源码复核）：当前 beforeSwap 已能按 URL 拒绝普通跨会话旧历史，不能描述为完全无守卫。缺口是 A→B→A 的旧代次、同会话不同 leaf/刷新乱序，以及 beforeSwap 之前的 HX 响应副作用；需要 generation/面板序号和 beforeOnLoad 统一守卫。原泛化的“两会话晚响应必覆盖”断言不作有效证据。 | `pi-webui-htmx/src/modules/workbench.ts:start/refreshHistory/gotoLeaf`；`src/modules/scroll.ts` |
| U18 | 中 | UI | 精度校正（本轮源码复核）：当前 beforeSwap 已按 sessionId 检查对话目标。尚缺同一 session 的旧代次/pending 集合乱序与响应处理前守卫；旧错误/finally 也可能影响新视图。按这些真实边界补回归，不再声称所有旧对话都会先 swap 再校验。 | `pi-webui-htmx/src/modules/workbench.ts:start/refreshDialogs` |
| U19 | 中 | UI | `ModelsEditor.save()` 成功后无条件 reload；保存等待期间用户继续输入的未保存草稿会被旧服务端快照覆盖。定向 Vitest 复现。 | `pi-webui-htmx/src/modules/models.ts`；`ui-model-save-probe.log` |
| U20 | 中 | UI | 附件上限只按每次 `addFiles()` 调用检查；两个并发批次各自看到旧的空数组，随后合并成 16 张，越过 8 张全局限制并放大内存/WS 拒绝。定向 Vitest 复现。 | `pi-webui-htmx/src/modules/attachments.ts`；`src/modules/workbench.ts`；`ui-attachments-limit-probe.log` |
| U21 | 中 | UI | `LiveView.appendThinking()` 每个 thinking delta 都读写整段 `textContent`，最高 40K 字符；token 级事件下形成重复整段复制，长推理会造成前端 CPU/GC 放大。 | `pi-webui-htmx/src/modules/stream.ts` |

## 补充发现（源码核对/定向复现）

| ID | 严重性 | 项目 | 问题与影响 | 主要位置 |
|---|---|---|---|---|
| B47 | ✅ 已修 | Storage | **修复：** 轮转文件按序号升序载入（当前 → .1 → .2 → .3），同一 requestId 只保留更晚结论。回归直接构造「新记录在 .1、旧记录在 .2」的布局。 | `internal/storage/receipts.go`；`receipts_test.go` |
| B48 | ✅ 已修 | Runtime | **修复：** 记录每个对话的登记时间，回收协程里清理超过自身 `timeout` 的对话并通知前端；无 timeout 字段的对话不误清。回归验证清理后 `busyLocked()` 为假、空闲回收恢复。 | `internal/runtime/dialogs.go`；`manager.go`；`dialogs_test.go` |
| B49 | ✅ 已修 | Management | 模型摘要按 Pi 数组计数并对总输出应用限额，稳定排序 provider，保留原始 modelCount 并标记截断；旧对象夹具已改为真实数组。 | `internal/management/config.go`；`config_safety_test.go` |
| B50 | 中 | Runtime | 同一 worker 的第二次 `Stop` 在 `closing` 后无条件等待 `done`；第一次强停超时但进程仍未退出时，关闭调用者可永久阻塞。 | `internal/runtime/manager.go` |
| B51 | ⚠️ 部分修复 | Management/Workspace | 配置读取已限制实际 reader 并检查打开的文件类型；workspace 文件/图片路径仍待修复，不能因配置侧完成就关闭此项。 | `internal/management/config.go`；`internal/workspace/files.go` |
| B52 | 中 | Sessions | 会话索引 `computeFingerprint`/`fresh` 不接收 context，且目录遍历本身没有目录数上限；取消请求无法中断指纹扫描，海量空目录也不受文件计数上限约束。 | `internal/sessions/index.go` |
| B53 | 中 | Tunnel | 浏览器帧上限为 1 MiB，relay 再加 `to`/`from` JSON 路由封装后仍受 1 MiB 读限；接近上限的合法本地帧会断开整条隧道。 | `internal/relay/server.go`；`internal/transport/tunnel.go` |
| B54 | 高 | Product | HTMX `BridgeClient` 固定连当前站点 `/api/v1/ws`，不实现 relay `/client` 登录、设备选择或路由封装；云端 UI 与本地桥的承诺部署链尚未连通。 | `pi-webui-htmx/src/modules/workbench.ts`；`pi-bridge-go/internal/relay/server.go` |
| B55 | 中 | Relay | relay 默认状态目录为系统临时目录 `/tmp/pi-relay`；设备注册表在重启/清理临时目录后丢失，长期部署必须显式指定持久 `--state-dir`。 | `cmd/pi-relay/main.go` |
| B56 | ✅ 已修 | Workspace | **修复：** stderr 限 32 KiB、不回显；取消进程组的真实后代测试通过。桥 SIGKILL 的整树保证仍属 B40/P7。原问题：`gitOutputLimited` 仅限制 stdout，stderr 使用无界 `bytes.Buffer`；同时 `CommandContext` 只回收 Git 直接子进程，仓库配置触发的外部命令后代可能存活。 | `internal/workspace/git.go` |
| B57 | ✅ 已修 | Tunnel | **修复：** pump 发送失败即标记 dead 并从映射摘除，同时释放订阅与终端；`acquire` 遇到死连接会替换。回归验证重连后拿到新连接且能收到响应。 | `internal/transport/tunnel.go`；`tunnel_test.go` |
| B58 | ✅ 已修 | Storage | **修复：** 打开追加句柄前把日志截回最后一个完整换行。回归模拟崩溃半行后追加并重开，校验每一行均可解析。 | `internal/storage/receipts.go`；`receipts_test.go` |
| B59 | 中 | Management | `npm:@scope/pkg@version` 的版本后缀未从 npm 包名剥离；已安装版本读取路径错误，registry URL 也把版本约束当包名。锁定版本夹具复现读取为空。 | `internal/management/packages.go`；`pinned-package-probe.log` |
| B60 | ✅ 已修 | HTTP | **修复：** 解析 qvalue，省略视为 1，未列出且无 `*` 视为不可接受，同名重复取最严格，非法 q 视为禁用。回归覆盖 `*`、`*;q=0`、`br;q=0`、同名重复与畸形 q。原问题：忽略 qvalue，`br;q=0` 仍选 br。 | `internal/presentation/presentation.go`；`compress_test.go` |
| B61 | ✅ 已修 | HTTP | **修复：** `Vary` 无条件声明；端到端测试按 8 种 Accept-Encoding 校验 Vary、Content-Encoding 与解压结果。原问题：仅压缩分支设置，identity 缺 Vary。 | `internal/transport/server.go`；`server_test.go` |
| B62 | ✅ 已修 | Sessions | **修复：** trash 存在却执行失败时报错取消，绝不退回 `os.Remove`；只有系统确实没有 trash 才真正删除。两条回归分别覆盖失败与缺失路径。 | `internal/sessions/delete.go`；`delete_test.go` |
| B63 | 高 | Relay | 非环回 `--listen` 配合默认空 `--host` 仍可启动；空 host 会同时跳过 Host/Origin 校验，且 HTTP listener 不强制 TLS，误部署可明文暴露认证令牌与 Cookie。 | `cmd/pi-relay/main.go`；`internal/relay/server.go` |
| B64 | 高 | Runtime | `SwitchSession` 先令 Pi 切到目标文件，再调用 `Rebind` 检查目标 worker 冲突；若目标会话已活跃，冲突发生时 Pi 已切换，旧键下的 worker 仍可 `Prompt`，可能形成双写。 | `internal/runtime/identity.go`；`internal/runtime/manager.go` |
| B65 | ✅ 已修 | Events | **修复：** `resetReplay` 同时更换 epoch；旧 epoch 一律拒绝并强制重新同步，不再用「返回空」假装已同步。new/switch/fork/clone 四条路径都经 `Rebind`，覆盖完整。反例（只归零 seq）验证通过。 | `internal/runtime/identity.go`；`identity_test.go` |
| B66 | 高 | Runtime/UI | Go 端所有命令统一由 `Manager.Timeout()`（默认 30 秒）取消；前端虽给 `session.compact` 设 120 秒等待，服务器仍在 30 秒结束调用，长压缩被报告为未知结果。 | `internal/transport/server.go`；`internal/runtime/manager.go`；`pi-webui-htmx/src/modules/bridge.ts` |
| B67 | ✅ 已修 | Runtime | **修复：** 对话登记移到体积上限检查之前，超大对话也占住记录并可回复；超出 `MaxDialogs` 时明确取消并推送说明。 | `internal/runtime/manager.go`；`dialogs_test.go` |
| B68 | ✅ 已修 | Runtime/Security | **修复：** Pi/PTY/Git 共用服务环境过滤；真实 spawn/PTY 测试和反向验证通过。保留正常 API、代理及 Pi 环境，不等同同 UID 的 OS 隔离。原问题：Pi 与 PTY 子进程直接继承桥的完整 `os.Environ()`，包括 `PI_BRIDGE_TOKEN`、`PI_BRIDGE_DEVICE_TOKEN`；agent bash、项目扩展或终端命令可读出桥/设备凭据。 | `internal/runtime/manager.go`；`internal/terminal/terminal.go` |
| B69 | ✅ 已修 | Management/Security | 写入使用随机独占 0600 临时文件、文件 Sync、rename 和目录 Sync；固定路径 symlink 不再被触碰。同步不明返回 outcome_unknown，临时文件统一清理。 | `internal/management/config.go`；`config_safety_test.go` |
| B70 | 中 | Product | HTMX 有模型配置原始 JSON 编辑和 discover/test，但没有调用已支持的 `config.catalog`；不是 Pi Web 式可视化模型字段编辑器，供应商目录/参数预设未接线。 | `pi-webui-htmx/src/modules/models.ts`；`pi-webui-htmx/src/templates/shell.html`；`pi-webui-htmx/src/types/protocol.ts` |
| B71 | 中 | Sessions | 已声明的部署限制：桥内单 writer 不能约束另一桥或不合作的外部 Pi CLI。保持独立会话目录；合作锁只约束参与者，不能写成已经防住全部外部写入。这是约束项，不是本轮新回归。 | `internal/runtime/manager.go`；`internal/sessions/store.go`；架构 S03 |
| B72 | 高 | Sessions | `sessions.search` 把请求的 `limit` 直接覆盖默认 `MaxMatches=100`，没有上限；`limit: 1000000000` 在 250 条夹具上全部返回，恶意大历史匹配可把结果切片撑至内存 OOM。定向探针复现。 | `internal/transport/server.go`；`internal/sessions/search.go`；`search-limit-probe.log` |
| B73 | 高 | Tunnel | 同一虚拟连接的命令分发为 goroutine 并发执行，但 `subs`/`terms` map 无锁；连续 subscribe/unsubscribe 在 `-race` 下触发并发 map 写。 | `internal/transport/tunnel.go`；`tunnel-map-race-probe.log` |
| B74 | 高 | Tunnel | 每条隧道命令建立新的 `release` channel，容量为 1 但不共享；因此没有每连接在途上限，且完全绕过 `Server.operations`，可无限堆命令 goroutine。 | `internal/transport/tunnel.go` |
| B75 | 中 | Terminal | `resolveShell` 只校验 basename，任意命名为 bash 的可执行文件可通过；与声明的 shell 白名单不符。显式 PTY 本来具有执行能力，这不是额外的任意执行提权结论；目标是使用本机固定真实路径表。 | `internal/transport/server.go`；`internal/terminal/terminal.go`；`shell-path-probe.log` |
| B76 | 中 | Export | 前端导出走 `command()`，会先 `ensureWorker()`；桥的 `session.export_html` 又要求 `manager.Get`。仅导出磁盘历史会启动 Pi 并加载整个长会话；Pi Web 的 `exportFromFile` 直接读 JSONL，不启动 AgentSession。 | `pi-webui-htmx/src/modules/workbench.ts`；`pi-bridge-go/internal/transport/server.go`；`pi-web/app/api/sessions/[id]/export/route.ts` |
| B77 | 中 | Export/Storage | HTML 导出持久写入 `stateDir/exports`，没有总字节/文件数上限、过期清理或下载后删除；不同合法文件名可不断占用磁盘。Pi Web 使用随机临时文件并在响应后删除。 | `cmd/pi-bridge/main.go`；`internal/transport/server.go`；Pi Web export route |
| B78 | 中 | Relay | `persist()` 在 marshal/write/rename 成功前就清 `dirty`；一次注入写失败后移除故障再重试，设备仍未保存，重启后丢失。确定性故障探针复现。 | `internal/relay/registry.go`；`registry-persist-probe.log` |
| B79 | 中 | Terminal | `terminal.resize` 只拒绝 0，未复用 `MaxCols`/`MaxRows`；接受最大 uint16 尺寸（65535×65535），超出 `Open` 的 500×200 上限。 | `internal/terminal/terminal.go`；`internal/transport/server.go` |
| B80 | 低 | Protocol | capabilities 将 `phase` 固定报 `A`，且 `terminals=4`/`terminalIdleSeconds=600` 固定写默认值；CLI 可通过 `--max-terminals`/`--terminal-idle` 覆盖，发现端点会向客户端报错限额。 | `internal/transport/server.go`；`cmd/pi-bridge/main.go` |
| B81 | ✅ 已修 | Management | 网页新增/修改 apiKey/header 命令表达式被拒绝，只允许按原身份保留本机已有值；Pi 的 `$!` 字面量转义不误判。Go 回归验证拒绝和保留，没有实际执行凭据命令。 | `internal/management/config_values.go`；`config_safety_test.go`；Pi `resolve-config-value.js` |
| D01 | 中 | Docs | 架构/阶段文档已标 A–E 完成，但 `README.md`、`docs/pi-compatibility.md`、`api/v1/protocol.md` 仍写 A 阶段或云隧道后续；前端 `docs/contract.md`、`docs/components.md` 仍记 Vite 7/旧 vendor 与“无前端测试”，和当前实现不一致。 | 两个项目的 README/docs、protocol 文档 |
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
| ⚠️ 部分覆盖 | 模型配置、models.dev 目录、历史分支树、文件预览、导出、设置、信任配置 | 模型配置目前是原始 JSON 编辑，`config.catalog` 未接线；树浏览需要 worker；PDF 等预览缺失；导出需 worker 且文件常驻；部分只读配置没有 UI |
| ❌ 未接通 | 云页面 → relay → 本地桥 | bridge tunnel 与 relay 后端存在，但 HTMX 客户端只连同源 `/api/v1/ws`，没有云端 relay 登录、设备选择和 route envelope 客户端 |
| 🚫 按用户决策不做 | OAuth 设备码登录、供应商额度查询、插件/技能远程安装/更新/搜索 | 用户明确只用 API 凭据、不查不可用额度，且远程安装插件等价于代码执行 |
| ⚠️ 暂缓/未立项 | Web Push 暂缓；PWA、版本检查/更新尚未立项 | 不把未实现推断成用户明确禁止 |
| ⚠️ 桥/UI 缺少或简化 | ChatMinimap、完整 PDF/文档查看、多语言、丰富斜杠命令面板、工作区 worktree | 参照 Pi Web 对应源码，按产品优先级决定，不能写成 Pi Web 也没有 |

## 审查完成度

两个仓库的生产 Go 模块、HTMX/TypeScript 入口和模块、模板、协议及主要资源生命周期已完成源码路径核对；主要跨层约束已与 Pi Web 对照。没有修改产品代码，所有问题保留为待修清单；每个 `高` 项修复时应把当前反例转成仓库回归测试。Pi Web 当前 checkout 的测试依赖不完整，因此对比结论来自源码而非该测试套件。

## 修复方案覆盖（2026-09-27，待实现）

完整设计、取舍与验收在 [architecture.md](architecture.md)，整体批次、依赖、代码影响及迁移以 [repair-plan.md](repair-plan.md) 的 P0–P7 为准。每个编号在下表有一个主要归属，共 105 个台账编号（B01–B81、U01–U21、T01、D01–D02），**不等于 105 个相互独立、全部运行复现的漏洞**；例如 B15/B74 是同一限流根因的不同表现。文档修订解决 D01 的描述漂移，但没有改变 B34/B80 的硬编码实现；D02 的独立 CI 工作流及显式跨仓联测入口已配置，本地验证通过，托管运行待首次接入远程；T01 已修并通过反向验证。U17/U18 已按现有 beforeSwap 守卫收窄描述，未新增或伪称运行复现。

| 方案 | 主要问题编号 | 决策与判定性验收 | 状态 |
|---|---|---|---|
| S01 共用 Executor / durable intent | B04、B15、B30、B31、B47、B58、B66、B74 | 先准入/claim/可靠 intent，再派发；并发同 ID、不同指纹、崩溃与轮转均不重复执行 | ⚠️ 待实现 |
| S02 原子 replay / Connection | B05、B14、B53、B57、B65、B73、U15、U16 | replay+订阅 fence；连接拥有取消资源；身份更换 epoch；双连接/重连/race 验证 | ⚠️ 待实现 |
| S03 身份事务 / 进程与删除 | B08、B09、B10、B40、B44、B50、B62、B64、B68、B71、B75、B79 | 先预留后切换，Stop 有界；删除收敛 writer；受监督 cgroup，明确外部 CLI 与同 UID 边界 | ⚠️ 待实现 |
| S04 配置 schema / 凭据 | B01、B02、B17、B32、B49、B51、B59、B69、B70、B81、U19 | revision/秘密操作/安全写；禁止重定向/新执行表达式；包并发上限；重排、并发保存和故障验证 | ✅ P1 基础秘密/安全写/出站；⚠️ revision、包、UI 等待实施 |
| S05 共用只读索引 / 扫描预算 | B06、B11、B12、B13、B27、B28、B29、B37、B38、B43、B52、B72、U04、U08 | 文件身份与完整行验证；标题/lazy/tree 复用；替换、50 MiB、深树、超多目录与取消测试 | ⚠️ 待实现 |
| S06 HTTP 大内容 / 导出 | B07、B33、B45、B76、B77、U05 | 小控制帧+有界资源；上传计算 Pi 编码；只读导出0 worker、临时配额与清理 | ⚠️ 待实现 |
| S07 UI SessionScope / 意图 | B03、U01、U02、U03、U06、U09、U10、U11、U12、U13、U14、U17、U18、U20、U21 | 目标发起时捕获、响应处理前守卫、并发预留；逐 await 切换、草稿/队列与 rAF 行为验证 | ⚠️ 待实现 |
| S08 扩展资源状态机 | B16、B36、B48、B67 | 分类/期限/写入回执；非法回复可重试，超大/过期/关闭不留幽灵 pending | ✅ B16/B48/B67 已修并回归；B36 待实施 |
| S09 relay 持久身份 / 部署 | B19、B20、B21、B22、B23、B24、B35、B41、B46、B55、B63、B78 | 持久与易失状态分离；TTL/连接预算/安全 Cookie/WSS；重启、写失败、撤销与 HTTPS 反代测试 | ⚠️ 待实现 |
| S10 受限 Git runner | B18、B25、B26、B42、B56 | 禁隐式 helper、流式预算、unborn 支持；标记脚本不执行、截断可见、后代收敛 | ✅ P1 实现与两仓联测 |
| S11 HTTP 缓存 / 内容资源 | B39、B60、B61、U07 | qvalue/identity/Vary 矩阵，先查缓存；反复挂载释放 URL/组件资源 | ✅ B39/B60/B61 已修并两仓联测；U07 待实现 |
| S12 云适配 / 能力与回归 | B34、B54、B80、D01、D02、T01 | 同源 HTTP+WS 云链路，方法/限额同源，版本协商，CI与隔离夹具 | ✅ P0 夹具/CI配置/联测；⚠️ 其余待实施 |

实施顺序采用整体规划 P0–P7；S 编号仍作为领域归属，不再维护另一份粗粒度施工顺序。每批验证全部受影响入口/调用方与迁移，允许边界固定后的 UI/只读叶子模块并行，不允许复制执行器或以旧危险路径作为回退；台账只在专项验收通过后关闭。
