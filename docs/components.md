# 组件选型、工具链与资源约束

更新：2026-09-30。当前实现以本页职责边界和 [F01–F19 落地记录](htmx-css-ts-review.md) 为准；带日期的历史测量不是持续验收结果。依赖版本以 `package.json` 和 `pnpm-lock.yaml` 为准，不升级依赖。[交互契约](contract.md) 区分当前实现与目标修复。

## 1. 保留现有技术栈

| 组件 | 当前声明版本 | 职责与约束 |
|---|---|---|
| htmx | ^2.0.11 | HTML 片段请求/替换；ESM 由 Vite 构建，入口显式挂 window.htmx |
| marked + DOMPurify | ^18.0.14 / ^3.4.16 | Markdown 解析后净化；不信任模型输出，不直接插入 marked 原始结果 |
| highlight.js | ^11.12.0 | 按扩展名/代码标签提示语言，避免自动误判；通过 lib/hljs 控制语言包 |
| KaTeX | ^0.18.9 | 公式与 auto-render 按需加载 |
| Mermaid | ^12.0.0 | 通过 Vite 动态 import 构建，图表出现才加载；不以裸包名绕过构建 |
| xterm + FitAddon | 6.0.0 / 0.11.0 | 有状态 PTY 字节流；动态加载，关闭/断线/dispose 语义分别处理 |
| ansi_up | ^6.0.6 | ANSI 转义输出；ANSI 内容不再交给 hljs 二次处理 |
| Tailwind / Vite 插件 | ^4.3.3 | 仅使用 preflight 复位；当前组件使用自定义类，不扫描模板生成工具类 |
| Vite / TypeScript | ^8.3.1 / ^7.0.2 | JS/CSS 代码分割、哈希与严格类型检查；不编译 Go 模板 |
| Vitest / jsdom | 5.0.2 / 30.1.1 | 真实模块行为测试，补充 Go 模板测试 |
| pnpm | 11.22.0 | 锁文件安装与脚本入口 |

上表是声明范围，不是“最新版本”报告；可复现安装使用 `pnpm install --frozen-lockfile`。第三方代码由包管理器管理，不直接改 node_modules 或保留另一套 CDN vendor。

## 2. 为什么不更换框架

审查暴露的主要根因是目标归属、交换时序、资源生命周期和跨层预算。换 React/Vue、改 SSE 或加入 Redux 都不会自动修复这些问题。保留 HTMX+小型 TypeScript 模块，用共用 SessionScope、请求序号与 disposer 明确管理状态。

| 选择 | 原因 |
|---|---|
| 不引入 UI/状态管理框架 | 模板与模块已能覆盖布局；目前需要的是显式状态所有权 |
| 继续服务端 diff 渲染 | 桥已有 Git 数据与 Go 模板；避免再下载/维护另一套 diff 渲染器 |
| 不引 diff2html | 基于职责和包体选择；不是“没有浏览器包/不能构建”的限制，项目已有 Vite |
| 小型 SVG 图标与 CSS 变量 | 五套主题继续复用，不为按钮引入完整组件体系 |
| 先分页，不上通用虚拟滚动 | 保留稳定回合/entry 锚点；超长回合仍需有界分段，不能无限扩大页 |
| i18n 等不先引框架 | 产品范围尚未立项，不能用“刻意不要”替代真实决定 |

“DOM 即全部状态”不再成立：session/draft、用户意图、请求 generation、服务端回执、组件资源都需要小型类型化状态；DOM 只是显示投影。

## 3. 构建和首屏预算

```bash
pnpm install --frozen-lockfile
pnpm test
pnpm typecheck
pnpm build
pnpm check
```

Vite 处理 JS/CSS；Tailwind 提供复位；Go 在运行时加载模板。桥通过 `dist/.vite/manifest.json` 解析哈希资源。发布时模板、manifest 和 dist 必须是同一构建，重建 UI 后重启桥。

- 首屏预算拆为两个都强制执行的数字：总预算 `build.firstLoadBudgetGzipKB`（**50 KiB gzip**）与自有代码预算 `build.firstLoadOwnBudgetGzipKB`（**30 KiB gzip**）。`build.vendorChunks` 声明哪些分块算供应商代码。
- 统计入口的全部静态依赖闭包；动态内容库不计入初始入口预算，但在第一次使用时仍真实消耗网络/内存。
- 2026-09-28 实测构成：自有代码 **22.65 KiB**（JS 15.66 + CSS 7.00）+ 供应商 htmx **17.59 KiB** = **40.24 KiB**。
- htmx 体积已对着包核实（2.0.11）：官方 `dist/htmx.min.js` 为 52,182 B / gzip 16,861 B；npm 包的 `main` 指向未压缩的 `dist/htmx.esm.js`（171,382 B），所以打包后 gzip 17.59 KiB，比官方压缩版多约 0.73 KiB（Vite 的压缩略弱于官方 terser 产物）。htmx 本身不是胖库，这一块属于换不掉的固定成本，因此单列。
- htmx 由 `vite.config.ts` 的 `manualChunks` 单独成块，桥在 shell 里为它输出 `modulepreload`：拆分不会多一个往返，同时我们改自己的代码不会顶掉它的缓存。
- 历史测量“页面+首次数据 brotli 34.9 KB”属于另一构建/资源集合，不能拿来当本次首屏新测量。也不把旧 Pi Web 资源数字当公平的持续性能对照。
- 不能为通过预算而只改数字；先检查静态依赖误入首屏、重复模块和不必要初始化，再决定范围。历史“无死代码”结论不作为长期保证；2026-09-30 再次清理模型旧样式和文件项重复规则，见 F12/F13。

## 3.1 职责边界：谁负责生成 HTML

htmx 侧重 HTML 与后端，因此边界按「数据 → HTML 归桥，瞬时交互留浏览器」划：

**归桥（服务端渲染片段 + `hx-*` 属性）**——凡是「把已有数据结构排版成 HTML」都属此类。
片段端点：`/ui/sessions`、`/ui/search`、`/ui/sessions/{id}/history`、`/ui/models`、`/ui/packages`、
`/ui/files`、`/ui/git-status`、`/ui/diff`、`/ui/branch`、`/ui/extensions/*`、`/ui/dirs`、`/ui/mc`、`/ui/mc/content`。
发现模型/测试连通使用认证 POST `/ui/models/discover`、`/ui/models/test`，结果由桥渲染，凭据不进入 URL。思考正文用原 lazy 路径的 `format=html` 变体。模板声明动作、目标与同步域；需调用 JS 时使用隐藏输入或 `htmx.ajax`，不再在 TS 中生成这些结果列表。

**顶栏面板的布局**（2026-09-28 对齐 Pi Web 源码）：

| 面板 | Pi Web 的结构 | 本仓 |
|---|---|---|
| 系统 | `.system-prompt-panel`：flex 列、高度 `min(600px,75dvh)`、内容 `flex:1` 滚动；正文 mono 12px、`pre-wrap`、不套边框盒子 | 同 |
| 工具 | `.tool-definitions-panel`：`grid-template-columns: clamp(112px,26%,220px) minmax(0,1fr)`；左栏工具名按钮（38px 行高、选中项 `inset 2px 0 var(--accent)`），右栏「描述 / 参数 / 提示词规则」三段，参数字段两列 `minmax(88px,.75fr) minmax(0,1.5fr)` | 同（少「提示词规则」段：`export_html` 的 tools 只带 `name/description/parameters`，不含 `promptGuidelines`） |
| 会话 | 三栏 `minmax(360px,1.7fr) minmax(140px,.55fr) minmax(190px,.75fr)`：左栏「会话信息 + 项目信息」（行带复制按钮），中栏消息，右栏 Token 与用量（右对齐、紧凑）；整块 mono 12px | 同分组，运行控件仍在输入栏，不重复一组 |
| 外层 | `position:fixed` 下拉，锚在顶栏下沿（`topBarRect.bottom`），`maxHeight: calc(100dvh - top)`，覆盖对话区 | 同语义的绝对定位 + `--topbar-h`；高度由 `ResizeObserver` 校正 |

**面板内没有标题行**（2026-09-29 对齐）。Pi Web 的这三个面板都是「内容直接铺满」，关闭靠再点一次工具栏按钮。因此：

- 壳层不放 `<header>`；只放一个绝对定位的关闭按钮（`background` 不透明以免压字，不占垂直高度），并支持 Escape 关闭（同时只开一个面板）。
- 会话弹层里的会话标题/工作目录**不再重复**：它们在侧栏里已经显示。`Workbench` 用内部 `sessionTitle` 保存会话名供重命名流程使用，不再往 DOM 里写。
- 模型 / 思考强度 / 工具预设不在这块面板里重复：输入栏各有一个控件（Pi Web 同样如此）。
- 工具预设只有输入栏一个选择器；面板里不再重复一份。

**会话弹层宽度实测对齐**（同一 SVG 尺寸下与 Pi Web 逐列比对）：

| 视口 | Pi Web 三栏 | 本仓三栏 |
|---|---|---|
| 1280×720 | 532 / 172 / 235（面板 1020 宽） | 相同公式，误差 < 8px |
| 1920×1080 | 890 / 288 / 393（面板 1620 宽） | 893 / 289 / 394 |

行距也一致：左栏事实 8px，消息与 Token 列 4px；面板内边距 `12px 16px`。

**三组的列模板必须写成分组选择器**（`.stats-info .stats-rows` / `.stats-message .stats-rows` / `.stats-token .stats-rows`），通用 `.stats-rows` 只放共用排布、**不得**声明 `grid-template-columns`。

踩过这个坑：通用规则曾写成 `.stats-grid > .stats-section > .stats-rows`（3 个类），特异性压过 `.stats-token .stats-rows`（2 个类），于是 Token 段的值列从 `max-content` 变成 `1fr` —— **数字被推到面板最右侧**，与 Pi Web 观感明显不同（Pi Web 的数值是贴着标签的）。`scripts/check-contract.mjs` 现在把「谁决定列模板」锁死；把通用规则改回会声明列模板的样子，检查会失败。

**Token / 用量是 Pi Web 的 compact 形态**，两条属性缺一不可：`grid-template-columns: max-content max-content`（列宽取内容）+ `justify-content: start`（整组靠左），值列内 `text-align: right` + `nowrap`。实测（1280 面板、同一份数据）：Pi Web `24.02px 7.20px`，本仓 `36.02px 60.02px`（差值来自本仓多一行「上下文」以及标签集不同，都是各自 `max-content` 的结果），gap 与 `justify-content` 一致。数值右边缘距该列右边缘约 110px —— 而不是贴到列的最右。

**数字格式逐条对齐 Pi Web**：千位分隔用 `toLocaleString` 语义；上下文窗口用 `formatCompact`（≥1e6 → 一位小数 + `M`，≥1000 → 整数 + **小写 `k`**，否则原样）；百分比一位小数；费用四位小数。千位后缀曾写成大写 `K`，与 Pi Web 的 `66k` 不符，已改。

**唯一未移植的行是「活跃时长」**：Pi Web 由前端从内存条目按时间戳累加，`get_session_stats` 不含它；桥要给出就得整份扫 JSONL，属于 B37 同类模式，故未做（补齐应在索引构建时累加）。

**模型选择器的可见文本改为只用模型名**（provider 进 `title`），与 Pi Web 一致；这也修掉了 390px 下 `DeepSeek Flash (test) · CPA-Responses` 被 `<select>` 硬截断的问题。

交互细节：工具列表的选中项与复制按钮是浏览器侧状态（切换不发请求，剪贴板只在浏览器），放在按需加载的 `panels.ts` 里；面板 HTML 全部由桥渲染。

**顶栏面板的数据来源**（2026-09-28 对着 Pi Web 源码核对）：

| 项 | Pi Web 的做法 | 本仓现状 |
|---|---|---|
| 完整历史 | 新标签页打开 `/api/sessions/{id}/export?inline=1`，即 Pi 自己的导出 HTML（内嵌完整 entries，有树导航） | 同语义：`session.export_html` + `/ui/exports/{name}?inline=1`。差别是**需要活动 worker** |
| 生成标题 | `generateSessionTitle()`：独立一次 one-shot 调用（短系统提示、无工具、同会话模型、最低思考等级、256 token 上限、90 秒超时），再 `setSessionName` | **不做**（已与用户确认）：手动命名足够；模型调用留给对话本身 |
| 从此处编辑 | 悬停用户消息 → `navigate_tree(targetId)` 改活动叶子 + 把该消息文本放回输入框 | **只做了只读一半**：`leafId` 视图 + 草稿预填。`navigate_tree` 不在 RPC 命令表里（只作为扩展的 `commandContextActions` 暴露），改叶子做不到 |
| 系统 | `state.systemPrompt`（进程内 SDK） | ✅ `/ui/system`：按需导出一份快照解析 |
| 工具 | `get_tools`（进程内 SDK）：`getAllTools()` + `getActiveToolNames()` 打 `active` 标记 | ✅ `/ui/tools`：同一份快照的 `tools` 即实际生效集合（RPC 无 `get_tools`） |
| 右上角会话面板 | `activeTopPanel === "session"`：会话/项目/消息/Token/费用/缓存命中/上下文分段表格 | ✅ `/ui/stats`：同样分组（会话/项目/消息/Token/运行/用量），数字全部来自 `get_session_stats` |

**已迁完**：`/ui/git-status`、`/ui/search`、`/ui/branch`（2026-09-28），随后是 `/ui/system`、`/ui/tools`、`/ui/stats`。

**片段端点的状态机约定**：htmx 换入的片段端点一律返回 200 + 可读 HTML，包括「worker 未启动」这类前置状态。原因是 htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容上且没有解释（真机复现过）。由服务端渲染状态（`RenderNote`）是 htmx 的用法本意。`/ui/file-text`、`/ui/file-image`、lazy 加载、`/ui/exports/*` 保留真实状态码；lazy 的 `format=html` 现在由 htmx 交换，读取失败也保留真实码，由全局错误提示反馈，不把失败 HTML 当成功正文。`branch.ts` 从 7.1 KB / 11 处 DOM 降到 3.4 KB / 1 处。

**已迁移后的边界**：顶栏事实表、目录/记忆导航、模型发现结果、思考正文归 Go 模板；模型配置保有单一浏览器草稿，其树是未提交文档的本地投影，属于明确例外。固定三态行来自 HTML template。思考等级下拉反映当前 WS 会话状态；文件预览只承担内容增强和容器交互。
扩展 widget 与附件缩略图保留浏览器：前者是 WS 推送的活跃状态，后者是尚未上传的本地 File。

**留浏览器**——只有转瞬即逝的交互状态，没有服务端等价物：
按键驱动的补全与斜杠菜单、滚动锚定、WS 流式增量、textarea 自适应高度、
xterm 终端、未上传的本地附件缩略图、markdown/高亮/KaTeX/ANSI 渲染管线、toast 通知。
判据是「这份数据在服务端有没有权威版本」：有就该渲染成 HTML 传下来。

这条边界是 2026-09-28 复核时收紧的：`git-status`、`search`、`branch` 三处原先在前端
用 `createElement` 重建，与桥的模板重复，且截断文案、层级缩进这类规则要维护两份。

一处刻意的例外：`/ui/file-text` 用 `fetch` 而不是 `hx-get`——它返回的是文件**内容**
（可能很大），直接交给渲染管线（高亮/ANSI）而不是交换进 DOM。

## 3.2 htmx 用法审查（2026-09-28）

对着 htmx 2.0.11 的源码逐条核对当前用法，结论与两处修正：

**核对通过的**：

- `hx-include="#hidden-input"` + `hx-trigger="<事件> from:body"`：自定义事件由 JS 触发，参数走隐藏输入。`findAttributeTargets` 解析选择器后逐个 `processInputValue`，对 `<input>` 直接取值。
- `hx-target="this" hx-swap="outerHTML"`（「加载更多会话」按钮）：分页替换自身的标准写法。
- `hx-target="#turns" hx-swap="afterbegin"` + `hx-swap-oob="innerHTML"`（「加载更早的消息」）：主片段前插、`#older-slot` 带外替换，两者在同一次响应里。
- `hx-sync="this:drop"`（「加载更早的消息」）：同元素在途时丢弃新请求，正是防重复提交该用的机制。
- GET 请求不会带上所属表单的值——`getInputValues` 里 `if (verb !== 'get')` 才 `processInputValue(..., getRelatedForm(elt))`。因此输入框里的草稿不会被拼进 `/ui/models` 的查询串。

**修正的两处**：

1. `#branch-body` 上曾写 `hx-disabled-elt="this"`。该属性给元素加 `disabled`，而 `div` 不响应 `disabled`，等于空转；已改为 `hx-sync="this:drop"`（同一元素在途时丢弃新请求），这才是它想表达的意思。
2. 片段端点的状态码：htmx 默认**不交换** 4xx/5xx。原先「worker 未启动」返回 409，界面会停在旧内容上且没有解释（真机复现）。已改为 200 + 服务端渲染的说明片段。见「片段端点的状态机约定」。

**载入提示不用 JS**：未指定 `hx-indicator` 时，`addRequestIndicatorClasses` 会把 `htmx-request` 加到发起请求的元素上（`indicators == null` 分支）。因此三个慢面板（system/tools/stats，都要先让 Pi 导出一份快照）只用 CSS 就能显示「正在读取…」并隐藏旧内容。刷新很快的片段不加，避免闪烁。

## 3.3 样式组织（2026-09-29 重理）

三个样式表，职责不重叠：

| 文件 | 内容 | 何时加载 |
|---|---|---|
| `src/styles/tokens.css` | **主题的唯一来源**：5 个主题的调色板、派生令牌、语义色 | 首屏 |
| `src/styles/app.css` | 组件样式。分节排列，末尾是响应式与无障碍 | 首屏 |
| `src/styles/code.css` | 代码高亮配色（highlight.js 的类） | 按需（有代码块时） |

**主题色值只允许出现在 tokens.css**：`app.css` 里写死颜色会让「跟随系统」与显式主题在某些组件上不同步。契约检查禁止 `app.css` 出现十六进制色值（黑色叠加层与终端底色除外，它们与主题无关）。

**深色主题有两个入口**（`[data-theme="dark"]` 与 `@media (prefers-color-scheme: dark)`），曾各自维护一份色值、改一处忘一处。现在两者都引用同一组 `--dark-*` 别名，契约检查核对两处的令牌集合完全一致。

**语义色**：`--danger` / `--success` 取代原先散落的 `#d13c45` / `#278353`。旧值在深色面板上只有 3.5:1，不达 AA；深色改用 `#ff7b72` / `#3fb950`（6.5:1）。

**对比度核算**：`pnpm check` 会跑 `scripts/check-contrast.mjs`，把 5 个主题 × 6 个文字色 × 2 种底色逐对算比值，低于 4.5:1 直接失败。踩过两次坑：mist 的 `--text-dim` 在面板底上 4.47、pine 的 `--accent` 4.45——都只差一点点，肉眼看不出来。

**代码高亮不再用厂商主题**。原先注入 highlight.js 的 `github-dark.css`（按需、非首屏），问题是：

- 它是固定色值，与 5 个主题（尤其 mist/rose/pine）对不上；
- 更严重的是它给 `.hljs` 画一块 `#0d1117` 深色背景，而我们 `<pre>` 在浅色主题下是 `#f9fafb`，于是变成「浅框套深块」，正文对比度只剩 **1.5:1**。

现在 `code.css` 用 `--code-*` 令牌（随主题切换），并显式清掉 `.hljs` 自身的背景。浅色取值在四个浅色主题的代码块底色上最差 4.95:1。

**右对齐的锚点必须挂在容器上**（2026-09-29 修）。顶栏的右对齐原本是 `#context-usage { margin-left:auto }`，而这个按钮在没有上下文数据时是 `hidden`——`[hidden]` 是 `display:none`，此时 auto 外边距完全失效，「就绪」与右侧两个按钮就跟着工具栏落到了栏中间（实测 x≈600，栏宽 1020）。

现在右侧四项（上下文用量、会话状态、会话菜单、工作区）包在 `.topbar-right` 里，由容器承担 `margin-left:auto`。契约检查核这条不变量：**顶栏里带 `hidden` 的元素不得靠 `margin-left:auto` 定位**（把它改回去会失败）。

同一模式在输入栏里是安全的：`.composer-actions` 用的是稳定的 `.spacer`（`flex:1`）做分隔，`#abort-button` 的 `margin-left:auto` 只在窄屏换行时把中止按钮靠右——它后面没有依赖对齐的兄弟节点，所以隐藏时无副作用。

**分节**：`app.css` 按「基础与主题 → 外壳布局 → 对话区 → 消息流 → 输入栏 → 通用控件 → 工作区面板 → …」顺序分节，响应式与无障碍放在末尾（覆盖规则靠后更确定，不必逐条核对顺序）。重排时用脚本核对过「选择器 → 声明」集合前后完全一致。

**清掉的死代码**（逐项在源码/模板/桥的 Go 模板里确认过）：

- `.panel-heading`、`.top-panel header strong`：面板标题行已随 Pi Web 对齐移除。
- `.facts` 系列：旧的 `#system-facts` 列表已被桥渲染的片段取代。
- Tailwind 的 `@theme` 颜色映射：模板里没有任何工具类，这条链路只用 preflight 复位；映射只会多写 19 个用不到的变量。
- 第二个 `@theme`（布局常量）与 `app.css` 的 `:root` 重复；`--chat-content-*` 两个令牌无人使用。
- `.session-action-grid` 有两份互相冲突的声明（grid 版被后面的 flex 版整体覆盖）。

`!important` 只剩 5 处：`[hidden]`、移动端 `visibility:visible`、以及 reduced-motion 的三条重置——都是必须压过内联或更高特异性的场合。

**顺带修掉的无障碍问题**（axe 从 2 项 violation 降到 0）：

- `page-has-heading-one`：侧栏品牌名从 `<strong>` 升为 `<h1>`（视觉不变）。
- `region`：「调整侧栏宽度」的分隔条原来与 `<aside>` 平级、不在任何 landmark 内；现在侧栏与分隔条包进同一个 `role="complementary"` 容器。grid 列数由包装层承担，布局不变（1280px 下侧栏 256 + 分隔条 4 = 260）。

## 4. 安全与 CSP

当前入口关闭 htmx eval/script 标签执行和 history cache；Go 模板转义、DOMPurify 净化另行负责。构建器看到 htmx 内部 eval 的警告不等于应用已走该路径，也不能因此取消内容净化。

深色主题是**深蓝基调**（`src/styles/tokens.css` 的 `[data-theme=dark]`，并镜像到 `prefers-color-scheme: dark`）：
底色 `#0f1720` / 面板 `#16202b` / 悬停 `#1e2b39` / 选中 `#24344a` / 边框 `#2c3d4f` / 正文 `#e4edf6` / 强调 `#7fb3e8`。
改色时保持相对关系即可：面板比背景亮一档、悬停再亮一档、强调色在深底上仍有正文级对比度。

模板里的缩进类布局不能用内联 `style`：片段受 CSP 约束，契约检查会拦住含 `{{ }}` 的 `style` 属性。
分支树的层级缩进因此用 `aria-level` + 静态 CSS 规则表达（上限 11 级，与 `internal/presentation` 的 `branchMaxLevel` 一致）。

目标 CSP 必须从真实资产/行为验证：同源脚本；blob/data 图片仅用于批准的图片路径；字体同源；连接限制到实际服务来源，不泛放所有 ws/wss。KaTeX、Mermaid、xterm 运行时样式的需要单独核验。**不再把旧文档的一段严格 CSP 当作当前已部署且可用的策略。**

文件预览、Markdown、图表、ANSI 等各用独立处理路径；没有“已经净化就可以忽略 URL/属性/资源权限”的捷径。

## 5. 资源所有权与目标修复

| 资源 | 目标所有者 | 释放时机 |
|---|---|---|
| fetch / htmx 读取 | 对应设备/会话视图/配置编辑作用域 + 面板序号 | 所属目标切换/替换时 abort，响应前仍检查归属；切聊天不取消全局配置草稿 |
| 图片 blob URL | 对应图片/预览组件 | 替换、清理片段、离开会话时 revoke |
| xterm、ResizeObserver、输入定时器 | TerminalPanel | 确认关闭后 dispose；断线仅禁输入，不谎称服务端已结束 |
| 附件异步读取/配额 | draft/session | 读前预留，成功转交，取消/失败释放 |
| live 文本/thinking buffer | 当前订阅 epoch | rAF 批量输出，settled/重同步/切换时释放 |

这些是 S07/S11 的待实现约束；当前 U07/U20/U21 等仍未修。htmx 片段替换也必须触发清理，不能只在整页卸载时释放。

## 6. 测试分层

测试文件和数量以 `pnpm test` 输出为准（本轮新增草稿、xhr 归属、目录失败、预览关闭和 Blob 释放反例）。TypeScript 管类型，Go 管模板/投影，契约脚本管结构与产物，Vitest 管异步/状态/生命周期，真实浏览器管交换/滚动/富内容行为。

新修复优先补：每个 await 点切换目标、beforeOnLoad 阻止旧响应副作用、并发附件预留、保存时继续编辑、深树迭代、组件重复挂载/卸载。基线绿不能替代这些反例；两仓独立 CI 已配置，本地联测通过；托管运行待接入远程，缺少配套 UI 的独立桥测试不算跨仓验收。
