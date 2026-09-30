# 剩余问题的联合分析与实施顺序

## 基线与证据范围

本轮基线：桥 `78c4ce4`、UI `ee92ca9`。用户要求先整体研究剩余问题之间的影响，再处理；本文件是实施前的源码核查与依赖分析，不把方案当作完成记录。Git 性能不在范围内。

核查路径覆盖当前台账未关闭项的入口、状态所有者及主要调用方，并复核与它们相邻的近期修复。以下新发现均为**源码确认**，没有在本轮运行并发导出、桥 SIGKILL 或跨机压力实验；已有性能数字仍引用 load-optimization-study.md 的隔离原型，不能当正式实现收益。

台账实际有 **106** 个编号（B01–B81 加 B35b、U01–U21、T01、D01–D02），并非旧文的105。原状态列有 **17** 项未关闭：

`U04 B36 B37 B38 B40 B43 B45 U15 U18 B51 B54 B55 B70 B71 B75 B76 D01`

其中 B71 是部署约束，不应混作待修 bug。因此原清单为16项行动项+1项约束。此次需重开 B63/U16/B77，得到 **19项既有编号行动项+1项约束**；另单列 R01 资源释放回归。O01–O04 是既有性能候选，B38 与 O03 是同一工作，不重复计数。B53 当前兼容性修复仍保留，但不能据此声称大帧资源治理完成。

## 1. 逐项校准

| 编号 | 当前源码证据 | 处理归属与边界 |
|---|---|---|
| U04 | `transport/server.go` `/ui/branch` 直接 `manager.Get → worker.Tree/ForkMessages`；UI branch.ts 已是 htmx 片段入口 | 磁盘树投影；浏览不启动 worker，fork 写操作仍显式启动 |
| B36 | `transport/extension_state.go` 只有全局 byKey；更新发生在 server.go 订阅转发协程 | 扩展状态放回事件生产侧，按 worker/session/epoch 隔离；只加 sessionId map 不能覆盖未订阅期间的状态 |
| B37 | `sessions/metadata.go:titleForPage` 为最新 session_info 扫全文件；同一索引版本已有 titleRead 缓存 | 共用扫描产物；不是“每次列表都无条件重扫”。冷读找任意位置最新标题仍需线性扫描 |
| B38 | `sessions/lazy.go:rawEntry` 从头读，history 已有 offset/size | 与 O03 完全合并，命中后同句柄定位并验证 ID/角色；冷路径保留限额 |
| B40 | `runtime/process_linux.go` 仅 Pdeathsig/Setpgid；已有 S03 明确服务管理器监督方案 | 受管部署与进程退出测试；不能用父进程的 defer 或 PID 枚举声称处理 SIGKILL |
| B43 | `sessions/search.go` 大文件跳过不增加 scanned；searchFile 无 ctx；打开后的 size 没再次与 MaxFileBytes 比较 | 访问预算与成功扫描数分开；逐行取消；同一打开文件上重新检查和限制读取 |
| B45 | 桥 `worker.ExportHTML` 透传；参考仓 `app/api/sessions/[id]/export/route.ts` 仍有三个树函数的迭代替换 | 与 B76 合做桥只读导出；不再往上游 HTML 拼字符串补丁 |
| U15 | `workbench.ts:onMessage` control 分支只处理 subscription_closed 后 return | 与 U16/扩展快照共用恢复协调器；不能只刷新 history 就声称恢复 pending/status |
| U18 | refreshDialogs 已单飞+dirty循环+scope，但 answerDialog 成功/错误之后未验证 scope；HTTP/RPC快照无共同版本 | 保留已有正确部分，补视图代次、集合版本和迟到结果守卫；不能退回每事件并行请求 |
| B51 | `workspace/files.go:Read/Image` 先 r.Stat，再 r.ReadFile，实际 reader 无上限 | 共用“打开→f.Stat→限额reader”路径，覆盖 HTTP 与 WS 两种入口 |
| B54 | relay 只有 WS路由；资源仍是 HTTP；UI 有大量根绝对路径 | 最后实施云适配；不能只改 BridgeClient 的 WS URL |
| B55 | `cmd/pi-relay/main.go` 默认 os.TempDir()/pi-relay | relay 持久身份/迁移与部署批次；旧目录发现及迁移不能静默丢用户 |
| B70 | `models.ts` 已有 provider/model 字段编辑；无 config.catalog 调用 | 状态应为部分；只补目录/预设接入，不再重写编辑器；结果进入现有本地草稿 |
| B71 | 外部 CLI 不参与桥内 worker 注册/锁 | 明确保留约束；只读路径不能假设桥是唯一 writer |
| B75 | `terminal.go:resolveShell` 校验 basename 后 exec.LookPath 或直接接受绝对路径 | 固定受信 shell 解析表，与默认 SHELL/符号链接兼容同时处理；不是撤销PTY的执行能力 |
| B76 | UI export 经 command/ensureWorker，桥 export 经 manager.Get | 磁盘导出，和 B45/B77 一次统一读取、写入与下载契约 |
| D01 | README称P1–P6完成，code-audit开头仍称P1实施中；下方限额表及方案表大量旧状态 | 最终按验收项维护；方案文档的目标态与实现记录分开，不再整阶段打勾 |

### 必须重新打开的三项

**B63：之前核错入口。** `cmd/pi-relay/main.go` 的 --host 默认为空，只做 listen 的 SplitHostPort；`relay/server.go` 的 Host/Origin 判断都以 `s.host != ""` 为前提。本地桥的 PublicOrigin/私网校验不能作为 relay 已修的证据。与 B55/B54 同域，但入口校验应先于云 UI 接通，不等待整条云链路。

**U16：前一轮只堵住事件帧切 epoch。** `workbench.subscribe` 的 begin 回调没有捕获 Scope/订阅请求序号；A发起订阅、切B、A确认返回时，仍会改B的cursor。`EventCursor.begin` 同 epoch 也直接覆盖 seq，需防止迟到确认使序号倒退。`accept` 在epoch为空时接受首事件，故“只有确认能决定epoch”的注释也不严格成立。桥 `subscribeWithReplay` 当前是“先补发→确认→实时转发”，不能简单禁止确认前所有事件，否则补发会丢。必须把握手排序与客户端状态机一起设计。

**B77：裁剪不是完整配额。** `dispatch.go` 仅在 pruneExports 时持 exportMu，随后解锁、Pi写新文件。单个新文件没有256MiB限制/预留；两个导出可同时看到空位；清理后没有worker仍会先删旧产物。必须把临时文件、写入限额、发布、清理与失败恢复放入同一资源协议；只扩大锁不能限制单次产物大小。

### 相邻回归 R01 与 B53 的实际边界

- R01：`workspace.ts` 新的 imageUrl 在 showPreview 换图时释放，但 file-close/dispose 没有 revoke；branch.ts 的监听支持 signal，但 workbench 创建 BranchNavigator 未传 signal，dispose 也不解除。随只读UI的生命周期批次修复，反例需包含关闭、销毁、切会话及异步导入完成后的卸载。
- B53：统一约97MiB接收上限修复了入口不一致，却提高了 relay 的最坏内存与队头阻塞。`enqueue` 对大帧取 max(4MiB, frameSize)，已有小帧时仍会超额并断连接；pumpWrites 在开始写前就扣 queued，所以“队列预算”不含在写帧。这是有界策略的实际含义，不等于严格4MiB保留内存。接入 B54 的 HTTP 大内容时应采用小块/credit/控制容量；不再通过加大 channel 解决。

## 2. 影响矩阵：哪些会互相破坏

| 修复组合 | 直接影响/错误的局部修法 | 联合方案与不变量 |
|---|---|---|
| U15 ↔ U16 ↔ U18 ↔ B36 | event_omitted 后简单reset让旧帧占据epoch；旧subscribe ACK重写新会话；pending刷新和状态缓存不属于同一worker | 固定 `(viewGeneration, sessionId, workerEpoch, subscriptionAttempt)`，恢复单飞、按需dirty合并；只恢复读取，不重发副作用 |
| B36 ↔ 订阅/worker退出 | 在每浏览器pump保存快照会重复更新、无订阅时漏记录，旧pump可覆盖重启后的状态 | 在worker消费Pi事件时记录，rebind/退出按所有者清理，快照总量受限；传输层只读取 |
| U16 ↔ replay顺序 | 直接把accept改成确认前全部拒绝会丢掉现行“确认前补发” | 优先保持v1顺序：重连补发只接受保存的epoch；新订阅在确认前隔离事件；同epoch确认不回退seq；如改线上顺序必须配套契约测试 |
| B37 ↔ B38/O03 ↔ U04 ↔ B76 | 各建一份索引，内存翻倍；标题页为每个会话构建全节点缓存又淘汰历史索引 | 共用扫描/文件代次表示，产物按需（小元数据与完整节点分级）；不缓存JSONL正文 |
| B71 ↔ 所有索引优化 | 假设只追加、仅按路径认身份，会把旧offset用于替换后的文件 | 打开句柄验证身份与大小/mtime；读前后核验，定位后再核entryID/角色；有限重试或conflict |
| B38/O03 ↔ user/tool-image边界 | 快速ReadAt绕过角色校验，用户附件暴露到工具通道 | 定位提速不改变Thinking/UserImage/ToolImage的角色与块下标校验 |
| B43 ↔ B37/搜索标题 | 搜索提早取消/达到访问上限时仍标完整；共享标题缓存把尚未扫描尾部当最新名称 | 访问/读字节预算与truncated明确；不完整扫描不得发布成完整索引或最终标题 |
| O04 ↔ 共用索引 | 固定四槽+len(nodes)*64无法证明严格堆上限；并发miss重复构建 | 先固定快照API和所有权，再测保留堆、并发构建与请求仍持有的旧版本；预算称估算不称物理硬上限 |
| B45 ↔ B76 ↔ B77 | 换成Go导出却继续经过ensureWorker，零worker目标仍失败；完成后再删大文件不能约束写入峰值 | Go流式转义投影、迭代树、受限writer、临时产物验证后发布；下载路径不依赖活动Pi |
| B51 ↔ B33/R01 | 图片改HTTP不等于后端读取有界；旧blob在关闭后仍持有 | 同句柄限制读取；HTTP保留4xx，前端按代次接收并释放URL，不通过片段200契约掩盖二进制错误 |
| B75 ↔ B40 | shell绝对路径白名单无法替代进程树清理；粗暴拒所有symlink破坏/bin→/usr/bin | 构造时解析受信路径表；服务管理器监督整组；手工启动能力边界另列 |
| B55/B63 ↔ B54 | 换状态目录让设备凭据“丢失”；路径前缀正确但资源仍借用错误设备cookie/权限 | 身份与持久目录先稳定，再做设备前缀和透明资源路由；URL/每标签页固定设备归属 |
| B54 ↔ B33/B76/htmx | 只代理WS后，图片/导出/片段落到云主机根路径；只改fetch漏掉hx-*、OOB、资产URL | 统一受信basePath注入/生成；服务端模板、fetch、WS、资源链接一并审查；relay不解析业务payload路由 |
| B70 ↔ 模型草稿/F01 | 目录选择后重读磁盘覆盖尚未保存的字段、***被当真凭据 | catalog返回服务端HTML，选中后按provider/model ID合入当前草稿，已有秘密和未知字段保持 |
| D01 ↔ 全部 | 总表更新而限额表/README仍旧，下一轮把“部分”误读为全阶段完成 | 编号保留历史描述，添加精确范围/证据/复测入口；从源码核对数字，不用通过测试数替代覆盖证明 |

## 3. 批次与前置条件

下列顺序替代“按编号逐个修”；各批可以拆成可编译提交，不要求所有同组改动挤进一个提交。

| 批次 | 范围 | 前置 | 必须通过的判定性验收 |
|---|---|---|---|
| A 视图与订阅归属 | U16重开、U15、U18；R01 | 本文核查完成 | A→B→A迟到ACK/错误/finally；确认前补发不丢；同epoch seq不倒退；100次关闭/卸载URL与监听回落 |
| B 扩展快照 | B36 | A的恢复接口固定 | 无浏览器时setStatus仍可读；同key跨worker不串；重启/rebind旧epoch不覆盖；总量有界 |
| C 有界文件/搜索 | B51、B43 | 可与A/B独立 | stat后增长/替换；最多读limit+1；超大文件计访问预算；单文件取消；HTTP/WS错误行为一致 |
| D 共用磁盘读取 | B37、B38/O03、U04；随后O04 | C预算语义；文件身份/快照接口 | 标题最新重命名；offset错位/角色保护；无worker分支树；深树有界分页；冷热和保留堆复测 |
| E 只读导出 | B45、B76、B77重开 | D | 0 worker；15000层不递归；取消无半成品；单项与全局字节/并发限制；已有下载不被错误清理 |
| F 进程与shell | B75、B40；B71边界 | 独立于D/E | PATH伪造bash拒绝，系统symlink可用；受管SIGKILL带setsid后代收敛；手工启动不虚报保证 |
| G relay基础 | B55、B63重开 | 可提前于D/E独立 | 持久目录迁移/重启；非环回空host拒绝；Host/Origin与TLS终止一致；不借本地桥测试替代 |
| H 模型目录 | B70剩余 | 保持现有草稿契约 | catalog结果按ID应用；不覆盖脏草稿/秘密；失败与超时不写配置 |
| I 云HTTP/WS整链 | B54及B53资源边界 | A/B/C/E/G，发布物/basePath约定 | 两设备两标签隔离；HTML/OOB/图片/导出/WS全通；慢下载时abort可达；取消/断线释放配额 |
| J 文档与范围验收 | D01及全部状态 | 随批更新，最终查重 | 当前实现/目标态/部署约束明确；能力/限额与真实配置一致；两仓配套版本可回放 |

O01（合并解码）、O02（独立工具预览）与上述功能修复没有硬前置关系，属于已排队性能工作，不应阻塞A/C/G。O01改变投影容错边界，须异常字段差分；O02仅截预览、正文/复制/导出保持完整。D/E确定投影接口后再合入O01/O02可减少重复适配。Git性能继续排除。

## 4. 实施纪律与未完成项

- 本研究没有宣布P1–P6全部完成。原README的该说法无足够验收支撑，应撤回。
- 本轮不把源码确认包装成新压测数据。新增问题先写有判定性的反例，红后改；每批测试覆盖本地WS与tunnel共享入口、真实模板及适用的三个视口。
- 序列化/大小兼容修复不等于资源预算完善：97MiB接收能力与4MiB队列策略需分列，不再写“公网附件已验收”。
- 配置CAS、v2协商、持久身份事务等architecture.md目标不能因编号表大多✅而自动算已实现；若实施触及相应边界，明确列为该批交付依赖。
- B54为既定未完成产品范围，规模较大；最后处理并单独验收，不再把“待立项”当作所有其余工作的完成理由。
