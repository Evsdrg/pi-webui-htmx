# Pi Bridge 架构与修复决策

核对日期：2026-09-27。**第 1–2 节描述现状；S01–S12 是本轮选定的待实现设计，不是交付声明。** P0 的测试基础建设与本地联测已完成，产品方案仍按批次待实现。审查基线为桥 `bd5a17e`、UI `e6fb298`；问题与证据见 [code-audit.md](code-audit.md)，在线契约见 [protocol.md](../api/v1/protocol.md)，跨层次序见 [communication.md](communication.md)。整体实施顺序、影响矩阵与迁移规则以 [repair-plan.md](repair-plan.md) 为准。

## 1. 保留的目标与边界

1. 优先适配 HTMX 工作台，直接读取 Pi 的持久数据，限制桥和浏览器资源占用。
2. Pi 以独立 `pi --mode rpc` 子进程运行，拥有 agent、模型上下文、工具、扩展及会话 JSONL 写入权。Go 桥不嵌入 Pi SDK，不另存一份会话正文数据库。
3. 列表、普通历史和文件浏览不启动 worker。分支树、导出目前仍依赖 worker，必须标为待解耦，不能用普通历史的验收替它们背书。
4. 网络断开不等于取消已受理任务；命令接受不等于任务完成；结果未知不能自动重发。
5. 模板与前端资源归 UI 仓，桥只加载可信发布物；模型输出、文件和标题按不可信内容处理。
6. Pi 工具和显式 PTY 是被授权的执行能力，工作区路径检查不是操作系统沙箱。隐藏在只读 Git/配置操作里的执行必须消除；清理子进程环境不能保证抵御同 UID 恶意进程。

## 2. 当前实现与依赖

| 现有位置 | 当前职责 |
|---|---|
| `cmd/pi-bridge`、`cmd/pi-relay` | 配置、装配和服务启动 |
| `internal/transport` | 本地 HTTP/WS、隧道虚拟连接、命令分发 |
| `internal/runtime`、`internal/pi` | worker/身份/扩展对话；RPC 分帧、关联与 stdin 写入 |
| `internal/sessions` | Pi v3 JSONL、目录/历史索引、搜索、惰性内容、删除 |
| `internal/workspace`、`internal/terminal` | 文件、Git、索引、PTY |
| `internal/management` | 模型配置、供应商探测、包清单 |
| `internal/events`、`internal/storage` | 有界事件队列与回执日志 |
| `internal/relay`、`internal/tunnel` | 用户/设备、配对、路由、主动外连 |
| `internal/presentation`、`internal/observe` | 模板/静态资源/压缩；指标 |

不创建旧图中并不存在的 `internal/app`、`internal/auth` 空包。优先在这些模块内提取小型共用对象；有独立职责与测试后再拆文件/包。保留 Go + JSONL 回执 + HTMX + TypeScript；本次不引入消息队列、通用 ORM、前端状态框架或 agent SDK。

## S01 — 共用命令入口与持久请求记录（待实现）

**问题根因：** 本地 WS 与 tunnel 重复编排限流、去重和生命周期，回执又在副作用之后才写。只在连接内加锁或扩大 seen 表不能解决跨连接/崩溃重复。

- 提取一个共用 Executor，复用已有业务分发，不重写每个方法。方法描述表分别声明参数解码、执行类别、持久策略、目标、预算和期限；capabilities 从同一配置生成。只读/订阅不写持久回执，PTY 输入不逐批 Sync；控制与 dialog 回复有独立语义，不能用一个 mutation 布尔量决定全部政策。HTTP 与 tunnel 共享准入和业务服务。
- 接入适配器只提供已认证主体、设备、连接代次和有界 writer。**取得每连接及全局配额后才启动任务 goroutine**；排队本身有上限。abort、强停、扩展取消使用独立但有界的控制预算。
- 去重键为 `(principal, device, requestId)`，请求指纹包括 method、不可变目标 session、规范化参数。相同 ID/相同指纹等待同一个结果或返回 pending；不同指纹返回 conflict。禁止落盘密钥和消息正文，指纹用摘要。
- 需要持久化的变更按 `验证/预留 → intent 落盘并 Sync → 允许派发 → 终态记录落盘` 执行。重复请求不能越过同一原子 claim；是否未派发由执行阶段判断，不能只从错误码推断 rejected。写 intent 失败时拒绝新持久变更；只读、状态查询及安全停止仍可用。临时输入/控制不承诺跨重启重放，也不自动重发。
- 重启发现 intent 没有终态，一律为 outcome_unknown；即便实际上尚未发往 Pi，也不擅自重发。Pi 不参与事务，因此承诺是**有效记录保留期内至多派发一次并显式保留不确定结果**，不是 exactly-once。
- 日志以单调记录序号和请求状态归并，先恢复最新有效状态；保留期内活跃 intent 不能被普通轮转/容量淘汰，恢复后转为有记录的 unknown 并遵循明示保留策略。空间不足就拒绝新持久变更。修复尾部半行后再 append；完整损坏记录进入 degraded/fail-closed，不悄悄丢掉去重依据。快照/轮转须临时文件、Sync、rename、目录 Sync，记录已验证的恢复起点；旧格式缺少主体/指纹/结果，按整体规划保守迁移。
- 对外声明实际保留窗口及存储健康；记录过期/被运维删除后不保证去重，客户端从不重用 ID，也不能把“未找到”当作“肯定没执行”。
- 连接 ctx 只控制等待与投递；受理后的任务 ctx 归 Executor/worker。方法超时分开：启动、只读、compact 等长操作各有期限；期限到只说明等待结束，不能伪称 Pi 已取消。控制调用不等长操作返回。

**取舍：** 先修已有有界日志，不为小规模回执引入数据库；付出一次可靠 intent 写入的延迟，换取可解释的崩溃语义。若未来改存储，沿用同一状态机而非另改承诺。

**验收：** 两连接同 ID 并发只派发一次；不同参数冲突；intent 前/后、stdin 写前/后、回执前/后注入崩溃；轮转、半行、磁盘满、存储故障均不重复派发；本地与 tunnel 共用同一组测试。

## S02 — 原子订阅与连接所有权（待实现）

- worker 在同一个短临界区校验 epoch/游标、取 replay 截止序号并注册 live 队列。临界区外按“确认 → replay 至 fence → live”输出，消除快照与订阅之间的空隙。重放与积压合计也受消息数/字节预算约束，超限明确 resync。
- epoch 标识 `(worker 实例, session 身份代次)`；启动、rebind 都生成新值，seq 只在同一 epoch 内递增。客户端只接受当前连接的订阅确认建立的 epoch，不能见任意事件就切回旧 epoch。当前 Workbench.subscribe 没有消费响应中的身份信息，实施时必须处理确认并建立订阅状态；未激活的订阅不直接派发业务事件，不能继续从任意事件猜 epoch。
- 本地与虚拟连接共用 Connection 对象，拥有 ctx、单 writer、发送队列、subscription/terminal attachment 的取消函数。map 访问统一加锁；锁内摘资源，锁外 cancel/等待，避免回调死锁。
- Close 幂等且有期限，退订必须调用底层 cancel。pump 失败触发完整 Close；从注册表删除时比较对象/代次，旧连接不得删掉新连接。同 clientId 重连创建新 Connection，绝不复用死 pump。
- 超限事件明确发有界 control；UI 收到 omitted/resync 立即废弃不完整 live 投影并重读持久历史。仍在生成的缺口保持可见，等待权威 message_end；不伪造内容。
- 业务帧与路由信封分别限额：按最坏编码长度预留 route 预算；不把一条合法大业务帧封装成非法 tunnel 帧。

**验收：** 在 fence 前后注入事件，重放+实时无遗漏/重复；并发订退阅通过 race；发送失败/同 ID 重连/旧连接迟到关闭后队列与配额均释放。

## S03 — worker 身份、删除与进程监督（待实现）

- Manager 按稳定 workerID 管进程，sessionID 只是可变索引。退出清理按对象身份移除全部绑定；Stop 幂等且每次等待均受 ctx/期限约束。PTY 使用相同“创建/关闭/退出”配额生命周期。
- switch/new/fork/clone 是身份事务：先在 Manager 预留目标或身份变更槽、确认不忙，暂停该 worker 新普通变更，再调用 Pi；成功后按 get_state 真实 ID/文件头提交绑定与新 epoch。stdout 持续读取，切换中的插件对话按稳定 worker/transition 归属应答；未知归属的普通消息不投到旧会话。失败释放预留；结果未知则隔离普通写入口并对账，不能在旧键下继续 Prompt。源/目标预留不覆盖长时间持锁 I/O，取消通道不等待 transition 结束。
- 会话通过 Store 的 ID→文件头/路径映射解析，不从 basename 猜 ID。fork/clone 产生未知新 ID 时，保留 worker 级 transition 状态并核验实际文件，发生冲突不将两名 writer 同时发布。
- 删除与启动共用 session 预留。默认拒绝有活跃 writer 的删除；显式强制删除必须先确认 worker/管道/子任务收敛。回收站失败就返回错误；永久删除须单独明确选项，不能自动从 trash 降级。
- Linux 正常退出按进程组 TERM→限时 KILL；`Pdeathsig` 仅是直接子进程的补充措施。生产部署优先由 systemd service cgroup 监督并使用 `KillMode=control-group`。**单独存在 cgroup 不会在桥死后自动执行清理，必须有存活的服务管理器负责停止单元。** 手工启动不承诺 SIGKILL 后全后代回收。要支持逐 worker 严格回收，再增加受委派子 cgroup，不能靠 PID 枚举冒充无竞态回收。
- Linux 属性置于平台文件；非 Linux 编译可通过但进程执行能力明确 unsupported，未完成前不声称支持。终端 open/resize 共用尺寸校验；shell 从本机配置的真实可执行路径表选择，网络只传预定义键。
- 子进程环境保留用户所需模型/代理变量，但统一移除桥、relay、设备和 Cookie 签名凭据；过滤覆盖 Pi、PTY、Git、导出辅助进程。不同权限必须用不同 UID/沙箱实现，环境过滤不是安全隔离。
- 对合作桥可加 session-dir 锁或稳定的独立锁文件；不要只锁会被 rename 替换的 JSONL inode。外部 Pi CLI 不遵守锁时依旧不能保证单 writer。默认隔离 agent/session-dir；共享目录是显式受限模式，发现冲突拒绝写入，不靠轮询 PID 猜锁。

**验收：** 目标已占用的 switch 绝不先切 Pi；每个身份变更后退出注册表归零；并发 start/delete；第二次 Stop 有界；systemd 模式带双重 fork/setsid 后代的崩溃测试。

## S04 — 同一份配置 schema 与凭据边界（待实现）

- 共用 Pi 基线配置投影：provider 按名字、models 数组按稳定 id 对齐；拒绝重复 id。`api` 是协议名、`baseUrl` 是 URL。摘要、脱敏、恢复和写校验共用 walker，未知非危险字段尽量保留，不把数组变对象。
- 所有 apiKey 以及自定义 header 值默认当秘密；只把明确安全展示字段放入视图。短期 v1 的 `***` 必须按稳定身份恢复，找不到原值就返回冲突，不能把占位符写为凭据。目标接口改为明确的 keep/replace/remove 秘密操作或绑定 revision/路径的 opaque secretRef，解决真实值恰为 `***` 的歧义。
- Raw 返回配置 revision；save 携带期望 revision，在配置锁内重新读盘比对。CreateTemp 同目录独占创建、0600、写入大小限制、Sync、rename、目录 Sync；不使用固定 `.tmp`，不跟随攻击者预置链接。对不合作的外部 CLI 写入仍不能承诺完全原子 CAS，冲突窗口/合作锁须明确。
- Pi 0.85.1 `resolve-config-value.js` 会执行以 `!` 开头的配置值。网页不得新增/修改命令型 apiKey/header，也不得任意引用桥环境秘密；本机已有表达式只以保留标记原样保存。discover/test 不执行命令表达式。桥对配置的受控写入不能变成隐式远程命令入口。
- discover/test 使用专用复用 HTTP client，**默认禁止重定向**，3xx 明确报错；凭据绑定原 origin，禁止降级到明文/跨源。端点准入由本机策略决定：公网 HTTPS 默认可用；本地模型/私有代理可显式授权，不能一刀切禁内网而破坏用户用例。禁止 URL userinfo、保留地址/元数据地址，连接时核实际解析地址，明确受信代理策略；仅检查 URL 文本不足以约束 DNS/代理。
- 包版本查询先规范化 npm scoped 名与版本约束；设置条数、总时长、并发和响应字节预算，单项失败只标该项。模型表单与 catalog 在 UI 编排，桥不替前端猜保存意图。
- 文件读取必须 open 后在 reader 上施加 N+1 上限；Stat 是预检，不是实际读取边界。

**取舍：** 不引入独立秘密数据库、不把真实旧密钥交给浏览器。管理端是显式高权限；对同 UID 或已授权 shell 不宣称秘密不可读。

**验收：** 模型重排/重命名/删除、大小写 header、占位冲突、两编辑器并发、symlink、写失败均不泄露或损坏凭据；重定向目标收不到任何自定义秘密头；命令表达式不会经网页新增。

## S05 — 共用只读会话索引与有界扫描（待实现）

- History、tree、标题、lazy 共用索引：entryId→offset/length/type/parentId，附最新标题/持久叶子等小元数据，不缓存正文。树提供有界平面节点页，UI 迭代遍历；活跃 Pi 内存叶子作为独立来源，不覆盖磁盘事实。
- 缓存以**已打开文件**的身份、size、mtime 与平台可用的 ctime 验证；页读取沿同一描述符，读取前后检查 generation。替换、截断或并发重写时失效并有限重试/返回 conflict，不能盲用旧偏移。元数据不能证明对抗性原地改写未发生；需要强校验的模式必须重新扫描/摘要验证，不能承诺零 I/O 的绝对正确。
- miss 时完整 LF 行先验证 JSON，再提取索引头；末尾未换行半条忽略，完整坏行显式诊断。早停解析只能省对象构造，不能绕过文档承诺的整行有效性。正确性成本用同夹具重新测量，不保留未经验证的旧速度倍数。
- scan cache 同时限制会话数、索引节点和总内存估算，计入构建中及被请求持有的旧版本；同文件代次并发 miss 合并构建，一个等待者取消不影响其余等待者。先保留小容量，测量后再决定是否扩展。titles/lazy 复用已验证索引；thinking/image 的定位键始终是 `(sessionId, entryId, blockIndex)`，不拿整轮最后 assistant ID 代替。
- 目录索引在 TTL 内直接返回快照，不每次整树计算指纹。桥自身变化立即 invalidation；外部新增会话允许 TTL 内最终可见，选中文件读前仍验证身份。先做清晰 TTL 策略，不急于引入 watcher。
- 使用分批 ReadDir/流式 Git 输出，目录、条目、字节、耗时、深度、并发均有上限。搜索逐行或定量字节检查取消；跳过超大文件也计入访问预算；超长行、目录/结果上限一律 `truncated` 加原因。请求 limit 只能降低服务端上限。

**验收：** 同 size/mtime 原子替换、原地改写、append 半行、完整坏行、旧 thinking 多块、50 MiB 多页与多 lazy、超多空目录、取消搜索；记录冷/热耗时、分配和缓存占用。

## S06 — 大内容走 HTTP，控制帧保持小（待实现）

- 维持现有小命令 WS，不为文件/图片/树统一放大帧。文件、图片、bash 完整输出、历史树分页、上传与下载走受鉴权的有界 HTTP；WS 只回元数据、游标或资源引用。现有 v1 方法先有界拒绝且保住连接，再协商新接口。
- 上传在暂存目录按 owner/workspace/draft 归属，发送时绑定不可变 session/request；校验实际字节、MIME、张数、总量和 TTL，先预留配额再读。WS 不再把整个附件数组 base64 化。
- Pi 仍需要 base64 ImageContent：发送前计算完整 RPC 字节，不能把上传成功误当作一定能进 stdin。候选默认：最多 8 张、单图及总原始图片不超过 4 MiB，文本/JSON 另留预算，并对完整序列化结果再次检查当前 8 MiB RPC 限额；这组值是**拟议值**，实现后由 capabilities 发布。
- 暂存文件有每用户及全局条数/字节、并发、TTL；引用不可跨用户/会话复用。已入执行的引用受任务租约保护，完成/取消/失败释放；崩溃残留在启动时回收，日志不保存 base64。
- 导出选择 Go 侧独立只读文档投影，复用已转义内容和迭代分支遍历；不启动 agent，不移植 Pi Web 的字符串替换补丁。导出格式可以与 Pi 原生 HTML 不同，需明确为 Bridge 导出，并测试内容/分支完整性。
- 若必须预生成下载文件，使用随机临时文件、单项/全局磁盘预算、完成后才给下载引用、取消/下载完成清理和 TTL 兜底；不让客户端指定任意导出文件名/路径。

**验收：** 当前曾断线的 600 KiB 文本/大图片可读取或明确有界拒绝且 WS 仍在线；近边界 base64/信封精确测试；导出 0 worker，15000 层树不递归溢出，取消与重启无长期磁盘增长。

## S07 — UI 的会话作用域与用户意图（待实现）

- 轻量 SessionScope 表示会话视图，持有固定 session/draft 身份、generation、AbortController 与 disposer；新 draft 绑定真实 ID 不等于另选会话。连接代次、工作区和全局配置草稿使用各自作用域，面板另有 request sequence，避免切聊天误关终端或丢配置草稿。异步命令在发起时捕获目标及选择，每个 await 后检查归属；已派发任务继续在原目标，迟到的成功、错误及 finally 都不得修改新视图。
- htmx 在 `beforeRequest` 记录作用域，`beforeOnLoad` 拦截过期响应（在 HX-Trigger/重定向等响应处理前）；`beforeSwap` 再检查目标。切换时 abort 旧读取，但 abort 不是取消已执行写命令。不可只在 Promise resolve/afterSwap 之后检查。
- 模型/思考选择与 worker 回读分为“用户意图/权威状态”，有 dirty/revision；启动后的默认回读不能覆盖首条消息的选择。模型保存提交 snapshot+revision，回来时草稿已变就只更新保存基线，不 reload 覆盖输入。
- 排队拆成两类概念：本次消息 destination=`steer|followUp`，两条队列各自 mode=`all|one-at-a-time`。不由 `followUpMode` 推导用户选择，也不为每条 queued prompt 隐式改模式。自动重试显示 unknown/本 worker 本地确认值，不能跨会话搬一个 checkbox 当事实。
- 附件按 draft 隔离；异步读前预留张数/字节、读后重新核验 generation 并释放预留。新查询输入发生时立即递增补全序号，不等 debounce；预览有固定容器，旧请求/图片卸载统一 dispose。
- live 文本/thinking 用有界缓冲与 rAF 批量 append，不在每个 token 重写全部 textContent。保存稳定 entryId+视口偏移；只在用户原本贴底时跟随流式，富内容增高再修正锚点。

**验收：** 对每个 await 点切会话；旧历史/对话/树/文件/补全响应无副作用；并行两批附件不超预算；首次模型与保存中新草稿保留；排队 destination 与 mode 独立测试。

## S08 — 扩展对话是有期限的资源（待实现）

- fire-and-forget 绝不进入 pending；status/widget/title 缓存按 `(worker, session, epoch, key)` 隔离并有总预算，rebind/退出清理。
- 对话先分类与校验，再决定展示是否可承载；过大/不支持的 dialog 必须经保留控制通道发 cancelled，不能省略事件却让 Pi 永久等待。
- pending→sending→forwarded/expired/cancelled 状态明确。先校验响应再尝试控制队列；队列满恢复 pending。确认 stdin 写成功后才能消费 pending；Notify 入队不等于送达。Pi 没有第二条业务 ACK，forwarded 只表示已发送，不宣称插件处理成功；写入结果不确定时显示 unknown 并由 worker 关闭/期限收敛。该路径有独立一次性 claim 和控制预算，不被普通持久变更堵住。
- 以 Pi 请求 timeout 建本地期限；只对已声明 timeout 的对话到期清理/尽力取消，传输延迟造成的差异要允许。editor 等无 timeout 对话不擅加短超时；显式取消或 worker 停止才结束。多个 pending、计时器和 waiting_input 由同一集合派生。

**验收：** 非法回复可修正重试；满队列、过大 dialog、Pi 自行超时、强停和 rebind 后无幽灵 pending/计时器；同名插件状态不串会话。

## S09 — relay 的持久身份与部署边界（待实现）

- 用户/设备凭据哈希、归属、撤销版本、配对码摘要/到期时间属于同一持久提交；online 与连接表是易失状态，不混进共享 Device 指针后异步序列化。不能把用户、设备和 token 分别提交后留下半配对状态。旧版只存在内存且已丢失的凭据无法迁移，必须明确重发/重新配对。
- 配对、领取、撤销、发凭据只有可靠写盘后才返回成功；锁内深拷贝快照与版本，I/O 成功后仅清对应版本 dirty，失败保留重试。预共享密钥明文只输出一次；状态目录持久、0700，文件0600。
- 本机 add-user/add-device 与服务进程使用同一状态目录锁；先采用**离线管理，服务运行时拒绝第二写者**，不增加公网安装/任意管理接口。
- TTL 在 Claim 时校验；一次性领取原子化。按 owner/来源地址及全局预算限流，code 分桶不是唯一保护；表项与连接有硬上限/过期淘汰。
- Cookie 使用受限 opaque subject 或编码后的结构化 payload+HMAC，显示名不作为点分隔字段。撤销同时失效凭据/现有连接，Close 遍历 tunnels 与 browsers。
- 非环回部署要求明确 external origin；反代只信显式配置的来源，不能相信任意 X-Forwarded-*。默认回源只绑定 loopback，cookie Secure 由已配置 HTTPS public origin 决定，Host/Origin 分别严格核对。**已实现（桥侧）：** `--public-origin` 声明对外来源后，桥额外接受该来源的 Host 与 Origin，`Secure` 跟随其 scheme；非环回监听只在该开关下放行，且只接受私有/overlay 网段地址（RFC1918、IPv6 ULA、链路本地、100.64.0.0/10）。WebSocket 的库层 origin 白名单必须与同一规则对齐——`coder/websocket` 默认要求 `Origin.Host == r.Host`，代理改写 Host 时会先拒掉桥自己已允许的来源。这是本地桥而非 relay 路径的实现；relay 侧仍按 B35 自行推断。
- 设备 token 放 WS upgrade Authorization，不放 query；非环回只允许 WSS。relay TLS 终止会看见转发明文，承诺只能是**不持久化正文、不记录秘密**，不是“接触不到模型密钥”或端到端加密。

**验收：** CLI 发凭据后重启服务可认证；过期/重复领取失败；写失败重试、race、撤销活连接、点用户名、HTTPS 反代、连接洪峰均有探针。

## S10 — 只读 Git 的受控执行（P1 已实现）

实现与五类 helper、子模块内联 diff、输出限额、取消后代的回归在 `internal/workspace/git*`。子模块返回提交摘要，不展开可能启动下一层 helper 的内联 diff。工作树与 Git 元数据根必须在授权范围内；全局 Git 配置不参与查询。边界是受信可执行文件及已审命令集合，不是同 UID 恶意并发修改元数据时的 OS 沙箱。

- 一个 Git runner 统一采用受信 Git 绝对路径、固定 argv、工作区校验和清理后的 Git 环境；禁止客户端注入参数。
- 覆盖 `core.fsmonitor=false`，diff 明确 `--no-ext-diff --no-textconv`；取消 pager、交互与可选写锁，过滤 GIT_EXTERNAL_DIFF/GIT_CONFIG_* 等注入渠道。核对受支持 Git 版本，不能把旧版本对布尔 fsmonitor 的解释当成新版本。实现还须检测 clean/process 等转换 helper 配置，逐项禁用或明确拒绝；不能把这三个开关等同关闭所有外部执行。以上不是任意 Git 子命令的通用沙箱，只开放已审过的只读集合。
- status 用可解析的 porcelain/分支元数据处理 unborn 与 detached HEAD。stdout、stderr、条目、字节、时间均有预算，截断必须返回 truncated；index 用流式 NUL 分隔读取，不能 Output 完整捕获后再限额。取消时收敛进程组。

**验收：** fsmonitor、external diff、textconv、clean/process 标记脚本均不被只读查询执行（无法安全处理时明确拒绝）；空仓库正常；10000 文件截断可见，stderr 洪峰与取消不会留下后代。

## S11 — HTTP 与内容资源生命周期（待实现）

- Accept-Encoding 解析 qvalue、wildcard、identity；q=0 禁用，identity 也不允许时返回明确不可接受响应。小响应阈值不能覆盖客户端禁止 identity 的协商结果。动态/静态/identity/304 都保持正确 Vary；Content-Encoding 与实际字节一致。
- 资产按发布 generation/路径/encoding 查有界缓存，命中不再读原文件；哈希不可变只对同一可信构建成立，UI 重建后重启桥原子加载模板+manifest+资产。
- blob URL、xterm、ResizeObserver、fetch、事件监听和计时器均归所属组件/SessionScope dispose；使用 htmx 清理事件与显式卸载，不能只依赖页面关闭。
- DOMPurify、Go html/template 转义、htmx 禁 eval/script 和 CSP 共同维持；富内容需要的 style/blob/font 权限以当前实现实测，不能文档写一个实际不可用的严格 CSP。

**验收：** q=0/identity/阈值/缓存矩阵；重复资源访问少读盘；100 次切换/展开后 URL、终端、监听器数量回落。

## S12 — 云链路、版本与验证（待实现）

- 选择**同源 HTTP + WS 透明适配**：云端按设备前缀路由，relay 在授权后把允许的 HTTP 资源请求和 WS 控制帧送往本地桥。UI 仍用同一 BridgeClient/htmx，只有受信 basePath 不同；不另写一套云专属 DOM 协议。
- tunnel HTTP 只接受资源 allowlist，不提供任意 URL proxy。使用 request/stream ID、有界 metadata 与 chunk、credit/取消、总量/并发/超时限制；控制帧保留容量，大文件不能挤掉 abort。UI 资产和模板来自同一发布包，设备选择按 URL/标签页隔离。
- backend `/client` 有路由封装不等于云 UI 可用。鉴权、WS、fragment、图片、导出、上传逐项双模式验收通过前继续标 ❌。
- 完整修复版按整体规划 P6 集中升级为 v2：订阅确认、请求状态、配置 revision/秘密操作与资源引用配套发布；P1–P5 可保持形状的修复仍按当前 v1 验证，不提前改版本或宣传能力。旧写入口明确拒绝升级提示，不保留旧盲写/旧去重逻辑作为回退。方法描述表驱动 limits/timeouts，不能把 phase=A 或默认终端数当实际配置。
- TUI SDK 独有能力、OAuth、插件远程安装等遵循 [兼容矩阵](pi-compatibility.md) 的范围决定。模型字段编辑/catalog 属于已要求而未完成项；PWA 等未决事项不擅自归入禁止项。
- 修复附正式回归测试；Go 假 Pi 按测试进程/构建内容隔离临时路径并清理，不共享固定可覆盖二进制。增加两仓 CI，联测真实模板包；合同检查只管结构/构建，行为交给单测与浏览器。

## 实施顺序与完成条件

实施批次统一见 [repair-plan.md](repair-plan.md) 的 P0–P7、依赖图与影响矩阵，替代原粗粒度 0–4 顺序。核心顺序为：基线/契约 → 独立高风险修复 → 共用执行/日志 → 生命周期/事件 → 前端联调与只读投影 → v2/数据通道配套发布 → relay 整链及部署验收。UI 作用域、只读索引和叶子模块可在约定固定后并行，入口/状态机改造沿同一集成线推进。

每个提交组先使准确反例转为正式测试，再完成所有入口和调用方迁移并删除旧分支；通过受影响回归后才能关闭台账。文档、核心代码、客户端、迁移与验收缺一仍为待完成。Journal 一旦接受新副作用，不能恢复旧日志快照来回滚代码；具体规则见整体规划。

## 核对资料

- 本地源码：上述模块、Pi 0.85.1 RPC/会话/配置解析实现、UI 安装的 htmx 2.0.11 `handleAjaxResponse`。当前桥 `MaxFrame=8 MiB`，`Client.read` 在超限后 fail；不能归因成 Pi 自身约 9 MiB 上限。
- [htmx 2 事件](https://htmx.org/events/)：beforeOnLoad 在响应处理前；与本地 2.0.11 源码及 Context7 2.0.4 文档交叉核验，不使用 htmx 4 的不同事件名。
- [Go HTTP Client 源码](https://go.dev/src/net/http/client.go)：重定向头转发与 CheckRedirect；自定义秘密头不能靠默认敏感头名单保护。
- [Git diff](https://git-scm.com/docs/git-diff)、[Git config](https://git-scm.com/docs/git-config)：external diff、textconv 和 fsmonitor。
- [systemd kill 手册源码](https://github.com/systemd/systemd/blob/main/man/systemd.kill.xml)、[cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)：受服务管理器监督的整组回收。
- [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html)：Accept-Encoding、qvalue、identity 与 Vary。

外部资料用于验证机制；版本、产品现状、资源数据仍以本机源码及可重复测量为准。
