# UI 包与交互契约

更新：2026-09-27。本文约定 UI 包/模板/浏览器行为；应用协议归 [Bridge Protocol v1](../../pi-bridge-go/api/v1/protocol.md)。[桥架构 S01–S12](../../pi-bridge-go/docs/architecture.md) 是修复决策，[审查清单](../../pi-bridge-go/docs/code-audit.md) 是未解决问题台账。**本文明确标记的目标约定尚待实现；文档更新不代表当前源码已具备这些保证。**

## 1. 所有权与实际目录

```text
ui-manifest.json             UI 包声明
package.json / pnpm-lock.yaml 依赖与构建入口
src/entry/app.ts              首屏入口
src/modules/                 WS、工作台、状态与惰性内容模块
src/types/                   协议与 htmx 类型
src/styles/                  Tailwind 入口和设计令牌
src/lib/                     打包边界辅助
src/templates/               Go html/template 模板
  extensions/                通用扩展状态/组件/对话
  partials/                  共用片段
dist/.vite/manifest.json     构建生成的资源映射
dist/assets/                内容哈希资源
tests/unit/                 Vitest 行为测试
tests/fixtures/             可控假 Pi
```

模板、CSS、JS、manifest 归 UI 仓；桥只读加载同一发布版本。旧的 `src/assets/vendor`、`src/client` 布局不再适用。桥没有内嵌 UI 兜底，也不在缺少产物时改走 CDN。

`--ui-dir` 未配置时桥只提供 API；配置后模板/manifest/产物缺失或不兼容应拒绝加载。资源表在启动时快照，重新 build 后需要重启桥；不要让新 manifest 配旧资源。Vite 只构建 JS/CSS，Go 模板仍由桥渲染，Tailwind 仅提供 preflight，不扫描工具类。

## 2. 模板基本规则

1. shell 是唯一完整 HTML 文档，其他模板只输出片段。
2. 所有用户内容经过 Go `html/template` 上下文转义，禁止转为 `template.HTML` 绕过检查。Markdown 在浏览器净化后显示。
3. 交互脚本放 TypeScript 模块；模板禁止内联脚本和 `hx-on` 求值表达式。当前入口已经设置 `allowEval=false`、`allowScriptTags=false`、`historyCacheSize=0`。外部哈希 script 由 shell 引用。
4. 动态值不写进 style；样式用 class/设计令牌。富内容库的运行时 style 需要另外核实 CSP，不能把模板禁内联等同浏览器完全禁内联样式。
5. 读取片段用 htmx；发消息、模型设置、终端、扩展回执等可由模块调用 WS。**不要求所有交互必须带 hx-*，也没有当前可用的 HTTP prompt/model 表单约定。**
6. 包清单只读，不渲染远程安装、更新、卸载入口。Magic Context 面板是独立的只读SQLite数据源，不是RPC插件UI的模拟实现。

## 3. 当前模板数据

下表按桥 `internal/presentation/presentation.go` 与 `extensions.go` 核对。字段类型以 Go 源码为准，模板和桥一起做渲染测试，避免维护另一套失真的结构体副本。

| 模板/数据 | 当前字段 |
|---|---|
| shell / ShellData | SessionID；JS、CSS 字符串列表 |
| sessions / SessionsData | Items、Selected、HasMore、NextOffset |
| SessionRow | ID、Title、Modified、Cwd |
| history / HistoryData | SessionID、LeafID、Turns、HasMore、OldestEntryID、HistoricalModel |
| Turn | ID、EntryIDs、UserText、UserImages、AssistantText、Steps、HasProcess、Thinking、Error、Usage |
| Step | Kind、Detail、EntryID、Images、Name、OK、Duration |
| models / ModelsData | Models、Current；ModelRow 为 ID、Name、Provider |
| packages / PackagesData | Packages；每行为 Name、Source、Version、Latest、HasUpdate、Disabled、Error |
| files / FilesData | Root、Entries、Truncated；每行为 Name、Path、IsDir、Size |
| diff / DiffData | SessionID、Files；DiffFile 为 Path、IsNew、IsDeleted、IsBinary、Lines |
| DiffLine | Kind、OldNo、NewNo、Text |
| extStatus / StatusData | Statuses |
| extWidgets / WidgetsData | Widgets |
| extDialog / DialogData | ID、SessionID、Method、Title、Message、Options、Placeholder、Prefill |

body 的 data-session-id 是当前显示目标，不得在长异步链中反复读它来决定已发起命令的目标。模型 Current 使用 provider/id。URL 中的参数仍要正确编码，不能因为路径已授权就跳过 URL 编码。

**已实现：** 思考占位符各自持有 entryId+blockIndex，不再用整轮的单一 AssistantEntryID 代表不同助手条目。搜索命中也能凭 Turn.EntryIDs 找到 user、assistant、tool 所属的回合；从搜索结果进入时只读展示截至命中条目的历史，并提供返回最新入口，发送仍沿会话当前分支继续。用户消息的图片保留为按 user entryId+blockIndex 惰性加载的占位，不在历史 HTML 中复制 base64；只有 user-image 入口可读该角色。Pi 以 `stopReason:error` 写下空 assistant 时，历史显示固定中文安全摘要，实时区不渲染上游原始错误或请求 ID。

`session.fork` 返回的 `text` 是待编辑的原用户消息，必须预填进新分支草稿；`persisted:false` 表示 Pi 已建立新 ID 但尚未创建 JSONL，此时保留活跃 worker、不请求磁盘历史，并明确提示关闭 worker 的丢失风险。页面刷新时只要 worker 仍在，UI 历史接口的 `204 + X-Session-Unsaved: 1` 让草稿继续可编辑，不能把它当作“会话不存在”。持久化后加载真实历史并移除临时提示；原会话未发送的草稿仍按原会话保存。桥不得伪造 JSONL 来提前持久化。

## 4. 历史与滚动

当前页按回合对齐，保留稳定回合/entry 标识；字节/条目上限优先于“整轮”。超长回合若需要跨页，目标是稳定 group/segment，而不是无限增加一页或重新折叠已显示节点。

- prepend 保留阅读锚点；append 是否跟随由用户原来是否贴底决定；replace 区分切会话与同会话对账。
- `X-Scroll-Mode` 是提示/诊断，**不再由桥强制决定滚到底部**。
- 目标锚点为 entryId+视口偏移；图片、公式、代码高亮异步改变高度后补偿。
- 页大小是原始条目数，不等于可见消息/回合数。磁盘 leafSource 与 live Pi 叶子必须分开显示。

## 5. 会话作用域（目标 S07）

使用轻量 SessionScope，不引入状态管理框架。作用域持有固定 session/draft 身份、generation、AbortController、资源 disposer；新草稿绑定真实 ID 不算另选会话。面板另有 request sequence；设备、连接、工作区和配置编辑使用独立作用域，具体所有权见 [整体规划](../../pi-bridge-go/docs/repair-plan.md)。切聊天不能丢全局配置草稿，同 cwd 切聊天不能误关 PTY；实际换 cwd 仍按当前行为关闭旧 PTY，关闭失败保留 ID 供清理。DOM 是投影，不是已发起操作的唯一事实来源。

### 5.1 命令

- 调用开始即捕获目标、文本、附件、模型/思考选择和队列 destination。ensureWorker 返回创建/恢复的绑定，不从新的 this.sessionId 推导目标。
- 每个 await 后检查作用域；切换前尚未派发的下一步写命令停止。已经派发的任务继续属于原会话，不能自动改投、取消或重发。
- 迟到结果可更新原目标的受限状态缓存，但不得改新会话 DOM、清空新草稿/附件。
- 新会话模型/思考选择作为用户意图单独保留；启动回读默认值不覆盖 dirty 选择。

### 5.2 htmx 与其他读取

2026-09-30已落实到 `fragment-requests.ts`：每个xhr的本地快照、beforeOnLoad守卫与会话切换取消。目录/模型发现使用独立revision。以下是规则，不代表所有RPC快照时序问题也已解决。

1. beforeRequest 把 session generation、面板序号、目标绑定到 xhr。
2. **beforeOnLoad** 先检查归属，过期则 preventDefault；该钩子早于响应 HX-Trigger/重定向处理。
3. beforeSwap 再检查目标节点和作用域，包括可能的 OOB 内容；过期不得交换。
4. 切换时 abort 旧读取作为节流措施，但不能用 abort 代替响应守卫。Promise 完成/afterSwap 再检查已经太晚。

history、branch、fork_messages、gotoLeaf、扩展对话、文件列表/预览、模型列表和搜索都遵循这一规则。补全输入一变就增加 sequence，不等 debounce 才作废旧结果。

### 5.3 草稿、附件和预览

- 草稿与附件按 draft/session 隔离。附件异步读取前预留张数/字节，读取结束核验 scope，失败/取消释放；并发两批不能分别越过全局限制。
- 目标上传使用受限 HTTP 暂存引用；当前仍是 base64 WS，不能声称已支持超过传输上限的图片。可接受值以桥实际 capabilities 和完整编码预算为准。
- 当前模型保存固定snapshot、完成不reload，后续编辑留在草稿；节点切换先收集字段。跨浏览器/CLI的revision/CAS仍是目标，当前协议未实现冲突合并。
- 预览固定容器承载文本或 img，关闭不依赖已被替换的子节点；blob URL、终端、监听、Observer、timer 都有显式 disposer。

## 6. 队列与状态

| 概念 | 取值/来源 |
|---|---|
| 本条消息 destination | steer 或 followUp，由用户选择 |
| steering 投递模式 | all 或 one-at-a-time，session.set_queue_mode kind=steering |
| follow-up 投递模式 | all 或 one-at-a-time，kind=followUp |
| 自动压缩 | Pi get_state 的已知字段 |
| 自动重试 | 无可靠 Pi 读回；unknown 或当前 worker 本地确认值 |
| 模型选择 | 历史页的 `historicalModel` 仅标记磁盘分支上的历史选择；当前模型以 worker 的 `session.state.model` 为准，`null` 或 `unknown/unknown` 不可作为发送目标。发送前显式选定的模型在启动 worker 后仍必须被应用；失败保留草稿。 |

destination 与 mode 不能互相推导：界面队列选项 `steering` 对应 `session.set_queue_mode.kind="steering"`，而 `session.prompt.streamingBehavior` 必须传 Pi 的 `"steer"`；`followUp` 在两处同名。选择目录与启动 worker 不得覆盖用户尚未发送的草稿、模型与队列意图。自动重试不跨会话复用一个 checkbox，不把未勾选当“Pi 确认关闭”。状态区区分连接在线、命令已受理、agent 运行、对话等待与未知结果。订阅关闭后的“正在核对任务状态”只在仍在核对时显示；核对完成就撤掉，`resync_required` 的缺口警告仍保留。

## 7. 事件与流式

- 当前 WS 有 requestId 关联、超时和断线处理；目标按当前连接的订阅确认确定 epoch，旧 epoch/旧 connection 的事件忽略。当前 subscribe 调用方尚未消费确认的身份数据，需显式建立订阅状态后再派发业务事件，必要暂存须有界。
- 收到 omitted/resync 立即标记 live 缺口并重读持久历史。重放只对保留窗口有效，不能伪造无损恢复。
- 文本与 thinking 使用有界 buffer+rAF 批量 append；不能每 token 复制完整已有 textContent。
- message_end 是权威内容；agent_settled 后对账持久投影；不只凭 agent_end 宣告全部结束。
- 非幂等命令 timeout/outcome_unknown 不自动重发；读取对账与取消是独立用户操作。

## 8. 通用扩展通道

| 类别 | 方法 | 行为 |
|---|---|---|
| 需回执 | select、confirm、input、editor | select/input/editor 回 value；confirm 回 confirmed；取消只回 cancelled |
| 无需回执 | setStatus、setWidget、notify、setTitle、set_editor_text | 展示/通知，不登记 pending |
| RPC 不转发/无效 | custom、setFooter、setHeader、终端/编辑器组件等 | 不渲染假能力 |

回执使用 session.ui_response 并固定请求所属 session/dialog，不把 value/confirmed/cancelled 一起塞进一个“通用回复”。对话框 data-dialog-id 与 SessionID 都必须正确。

目标 S08：状态缓存按 worker/session/epoch/key 隔离；超时/过大/取消都有终态；pending 回复校验或队列失败不能先吃掉对话。切换会话时插件对话仍按固定 worker/transition 应答，不能被身份屏障堵住。stdin 写成功只显示已发送，不能宣称 Pi 已处理。轮询只更新变化的 pending 项，不覆盖正在输入的值。editor 没有声明 timeout 时不擅加短超时。

## 9. 构建、发布与版本

- 当前工具链为 pnpm 11、Vite 8、TypeScript 7、Tailwind 4；精确版本以 package.json/pnpm-lock.yaml 为准。Vitest+jsdom 已使用。
- 首屏预算当前 40 KiB gzip，按入口静态依赖闭包累计；KaTeX/Mermaid/xterm/Markdown 等按实际导入边界惰性加载。不能只数入口或用总 dist 体积代替首屏。
- 桥读取 ui-manifest、验证 protocolVersion/requiredMethods，再读取 Vite manifest 解析 JS/CSS。路径按各 manifest 的定义解析，不能把 dist 资产当 src vendor。
- 没有“内嵌备用模板/CDN 缺库回退”；缺依赖/产物应明确失败。
- 新增可选字段可以协商；完整修复按整体规划 P6 集中升级 v2，并一起更新 manifest、模板、TS、桥和测试工具。当前代码仍是 v1，没有提前改变版本。requiredFeatures 等协商字段尚未实现；旧标签页遇到版本不符须保留草稿并停止新写操作，不回退旧危险接口。
- 本地与云端目标共用 HTTP/WS UI，只改变受信 basePath。当前云端 relay UI、上传数据通道、磁盘树/导出尚未交付。

## 10. 验收分工

| 层 | 负责 |
|---|---|
| Go 渲染测试 | 真实模板字段、转义、片段形状、边界 projection |
| TypeScript/Vitest | 目标捕获、迟到响应、用户意图、队列语义、附件预算、dispose |
| 契约检查 | manifest、模板结构、产物引用、gzip 预算 |
| 浏览器 | 真实 htmx 交换顺序、HX 响应副作用、滚动、富内容、移动布局和闭环 |
| 本地/云模式联测 | 认证、片段、WS、图片、上传/下载、断线恢复一致 |

当前 72 项基线通过不意味着新增反例已修。修复必须把审查反例转为正式测试；不得以字符串正则代替行为接线验证。