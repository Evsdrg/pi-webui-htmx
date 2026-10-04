# 开发说明（前端）

本文件是前端开发的唯一入口：目录结构、技术栈、开发循环、样式与安全约束、常见坑。
面向使用者的安装说明见[桥的 README](../../pi-bridge-go/README.md)；
桥的接口契约见 [protocol.md](../../pi-bridge-go/api/v1/protocol.md)。

## 目录结构

```text
src/entry/app.ts     入口：装配各模块、按需运行增强
src/modules/         工作台与交互模块（见下表）
src/templates/       Go 模板；桥在运行期渲染，浏览器只接收片段
  extensions/        通用扩展状态/组件/对话
  partials/          共用片段
src/styles/          tokens.css / app.css / code.css 三表
src/types/           protocol.ts（v1 协议类型）、htmx.d.ts
src/lib/             url.ts（basePath 自适应）、hljs.ts（高亮按需加载）
scripts/             check-contract.mjs / check-contrast.mjs
tests/unit/          Vitest 行为测试（jsdom）
tests/fixtures/      可控 DOM/协议夹具
dist/                Vite 产物，不提交
```

核心模块：

| 模块 | 职责 |
|---|---|
| `workbench` | 主控：会话选择、发送、命令调度、状态投影 |
| `bridge` | WS 连接、在途请求、重连与游标；不保存完整会话 |
| `stream` | 当前轮次的事件累积与 live 视图；历史仍由桥分页渲染 |
| `fragment-requests` | HTTP 片段归属守卫：在 HX 响应头/OOB 处理**之前**拒绝旧响应 |
| `scope` / `layout` | SessionScope 与面板/侧栏布局、主题、草稿 |
| `workspace` / `terminal` | 文件树与 PTY；终端在用户打开时才动态导入 xterm |
| `markdown` / `math` / `mermaid` / `highlight` / `ansi` | 富内容：全部按需加载，输出经净化 |
| `models` | 模型配置编辑器（浏览器持有单一草稿，桥负责落盘校验与秘密保留） |
| `scroll` / `lazy` / `attachments` / `mention` / `toast` / `panels` / `topbar` / `branch` | 滚动锚定、惰性内容、附件、@ 补全、提示、顶栏面板、分支导航 |

## 技术栈

| 组件 | 职责与约束 |
|---|---|
| htmx 2.0 | 片段请求/替换；入口显式挂 `window.htmx`，已关闭 eval/script 标签与 history cache |
| marked + DOMPurify | Markdown 解析后净化；模型输出按不可信处理，不直接插入 marked 原始结果 |
| highlight.js | 按扩展名/语言提示高亮；语言包由 `lib/hljs` 控制，配色走 `--code-*` 令牌 |
| KaTeX | 公式，检测到分隔符才加载 |
| mermaid | 图表，动态 import（传递依赖 `elkjs` 是 EPL-2.0，见根 README 的许可证节） |
| xterm + FitAddon | 有状态 PTY 字节流；动态加载，关闭/断线/dispose 语义分开 |
| ansi_up | ANSI 输出；ANSI 内容不再交给 hljs 二次处理 |
| 自有 CSS reset | **替代通用 CSS 框架的基础复位**——模板里没有 utility class，组件样式全部由仓库手写 CSS 提供 |
| Vite 8 / TypeScript 7 | 代码分割、哈希与严格类型检查；不编译 Go 模板 |
| Vitest + jsdom | 模块行为测试，补充 Go 侧模板测试 |

**不引入 UI/状态管理框架**：要解决的是目标归属、交换时序与资源生命周期——换框架不会自动修复这些。
「DOM 即全部状态」不成立：会话/草稿、用户意图、请求代次、服务端回执、组件资源都需要小型类型化
状态，**DOM 只是显示投影**。

## 开发循环

```bash
pnpm install --frozen-lockfile
pnpm dev          # vite build --watch，改 src 后自动重建 dist/
# 另一个终端：桥指向本目录
cd ../../pi-bridge-go && go run ./cmd/pi-bridge --ui-dir ../pi-webui-htmx ...
```

⚠️ **重建 `dist/` 之后必须重启桥**：桥在启动时读取 `ui-manifest.json` 并快照入口资源名，
不重启会继续引用旧哈希的 assets（页面刷新无效）。这是最容易浪费时间的坑。

## 构建与产物

`pnpm build` = `tsc --noEmit` + `vite build`。产物里的三份清单必须与桥对齐：

| 文件 | 内容 |
|---|---|
| `ui-manifest.json` | 协议版本、必需方法、首屏预算、入口资源名 |
| `dist/.vite/manifest.json` | Vite 的 chunk 图 |
| `dist/assets/*` | 带内容哈希的实际资源 |

桥启动时校验 `protocolVersion` 与 `requiredMethods`，不匹配就明确失败——**没有内嵌模板或 CDN 回退**。

首屏预算是硬约束，两个数字都在 `ui-manifest.json` 的 `build` 里：总 **50 KiB**、自有代码 **30 KiB**
（均为 gzip）。`pnpm check` 递归统计入口的静态依赖闭包，并按 `vendorChunks` 把供应商分块单列——
htmx 是换不掉的固定成本（gzip 17.59 KiB），单列才能让改 UI 时只盯自己的额度。KaTeX、Mermaid、
xterm 等按需加载，不计入首屏。

## 职责边界

**判据**：这份数据在服务端有没有权威版本？**有就渲染成 HTML 传下来**，不要用 TS 拼 DOM。

归桥（服务端片段 + `hx-*` 属性）：

- 片段端点：`/ui/sessions`、`/ui/search`、`/ui/sessions/{id}/history`、`/ui/models`、`/ui/packages`、
  `/ui/files`、`/ui/git-status`、`/ui/diff`、`/ui/branch`、`/ui/extensions/*`、`/ui/dirs`、`/ui/mc`、
  `/ui/system`、`/ui/tools`、`/ui/stats`
- 模型发现/连通测试用认证 POST（`/ui/models/discover`、`/ui/models/test`），**凭据不进 URL**
- 思考正文走 lazy 路径的 `format=html` 变体
- 模板声明动作、目标与同步域；需要 JS 参与时用隐藏输入或 `htmx.ajax`
- **会话列表的两个视图**：`/ui/sessions?view=timeline`（默认）与 `view=workspace`。
  默认时间线是刻意的：看到全部会话不该先做一次选择。分组视图每组只带最近 5 条
  （`sessions.GroupedPerCwd`），点组标题或「查看全部」切回时间线并筛到该工作区。
  两个视图共用 `sessionRows`，同一会话的标题取值与时间格式必须一致；
  组名沿用下拉的短名规则（`cwdLabels`），同一个工作区在两个视图里必须同名。
  视图控件用**按钮而不是 select**：浏览器在 reload 时会恢复表单控件的值，
  恢复出来的值与偏好无关，控件显示与列表内容就会对不上（真机复现过）。
  选中态与随请求发送的 `view` 用隐藏输入，来源只有一个——本地偏好，
  片段里由 OOB 整块换掉控件（例如从分组视图点「查看全部」切回时间线）。
- **侧栏工作区筛选的下拉选项**：`/ui/sessions` 与 `/ui/search` 接受 `cwd`，候选项与计数随片段用
  `hx-swap-oob` 一起更新。候选不交给前端收集——列表是分页的，前端只看得到当前页里的 cwd，
  据此生成的选项会漏掉其余会话，计数也是错的。短名由桥删掉公共目录前缀得出
  （`cwdLabels`：`/opt/projects/alpha` 与 `/opt/projects/beta` → `alpha`、`beta`），
  完整路径放在 `title` 里；`<option>` 的文本不像普通元素那样能靠 CSS 截断。

留浏览器（只有瞬时交互状态，服务端没有权威版本）：按键驱动的补全与斜杠菜单、滚动锚定、
WS 流式增量、textarea 自适应、xterm、未上传的本地附件缩略图、富内容渲染管线、toast。

一处刻意例外：`/ui/file-text` 用 `fetch` 而不是 `hx-get`——它返回文件**内容**（可能很大），
交给渲染管线而不是交换进 DOM。

**片段端点的状态码约定**：htmx 换入的片段**一律 200 + 可读 HTML**，包括「worker 未启动」这类
前置状态——htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容且没有解释（真机复现过）。
保留真实状态码的例外：`/ui/file-text`、`/ui/file-image`、lazy 正文、`/ui/exports/*`（内容读取
而非片段交换），以及 `/ui/sessions/{id}/history` 的 `204 + X-Session-Unsaved`、`/ui/extensions/dialog/{id}`
的 `204`（状态信号，调用方在 `htmx:beforeSwap` 里读）。

## 样式：三张表

| 文件 | 职责 | 加载 |
|---|---|---|
| `src/styles/tokens.css` | **主题的唯一来源**：5 套调色板、派生令牌、语义色 | 首屏 |
| `src/styles/app.css` | 组件样式，分节；末尾是响应式与无障碍 | 首屏 |
| `src/styles/code.css` | highlight.js 配色，颜色全走 `var(--code-*)` | 按需（由 `lib/hljs` 引入） |

`scripts/check-contract.mjs` 强制以下不变量（改坏会直接失败）：

1. **主题色值只允许出现在 `tokens.css`**；`app.css` 出现十六进制色值即失败（与主题无关的叠加层除外）。
2. 深色主题两个入口（`[data-theme=dark]` 与 `prefers-color-scheme`）的令牌集合必须**完全一致**。
3. `.hljs` 背景必须 `transparent`；不得引入 highlight.js 自带主题（浅色主题下会变成「浅框套深块」）。
4. 会话详情的列模板**只能由分组规则**声明（`.stats-info/.stats-message/.stats-token .stats-rows`）；
   通用 `.stats-rows` 不得声明 `grid-template-columns`——特异性反转让 Token 数值被推到面板最右。
5. Token 组必须是 `max-content max-content` + `justify-content: start`（Pi Web 的 compact 形态）。
6. 顶栏里**带 `hidden` 的元素不得靠 `margin-left:auto` 定位**；右对齐由 `.topbar-right` 容器承担。
7. 模板里不得出现根绝对路径（`hx-*`/`href`/`src`）；不得含动态内联 `style`（CSP）。
8. 自定义属性被引用前必须已定义。

对比度由 `scripts/check-contrast.mjs` 校验：5 个主题 × 6 个文字色 × 2 种底色逐对算比值，
低于 4.5:1 直接失败。

## 安全与 CSP

- 所有用户内容经 Go `html/template` 上下文转义；**禁止转为 `template.HTML` 绕过检查**。
  Markdown 在浏览器净化后显示。shell 是唯一完整 HTML 文档，其他模板只输出片段。
- 模板禁止内联脚本与 `hx-on` 求值表达式；动态值不写进 `style`。分支树层级用 `aria-level` +
  静态 CSS 表达（上限 11 级，与 `internal/presentation` 的 `branchMaxLevel` 一致）。
- 入口已关闭 htmx 的 eval/script 标签执行与 history cache；**构建器对 htmx 内部 eval 的警告
  不等于应用已走该路径**，不能因此取消内容净化。
- 目标 CSP：同源脚本；blob/data 图片仅限批准的图片路径；字体同源；连接限制到实际服务来源，
  不泛放 ws/wss。KaTeX、Mermaid、xterm 的运行时样式需要单独核验。
- 包清单只读，不渲染远程安装/更新/卸载入口。

## 资源所有权

| 资源 | 所有者 | 释放时机 |
|---|---|---|
| fetch / htmx 读取 | 设备或会话视图 + 面板序号 | 目标切换时 abort；响应处理前仍检查归属；切聊天不取消全局配置草稿 |
| 图片 blob URL | 预览组件 | 替换、片段清理、离开会话时 revoke |
| xterm、ResizeObserver、输入定时器 | 终端面板 | 确认关闭后 dispose；断线只禁输入，不谎称服务端已结束 |
| 附件异步读取与配额 | 草稿/会话 | 读前预留，成功转交，取消/失败释放 |
| live 文本与 thinking 缓冲 | 当前订阅代次 | rAF 批量输出；settled/重同步/切换时释放 |

htmx 片段替换同样触发清理，不只依赖整页卸载。附件按草稿隔离，异步读取前预留张数/字节，
读取结束核验作用域；并发两批不能分别越过全局限制。

## 交互约束

作用域：调用开始即捕获目标（会话、文本、附件、模型/思考选择、队列 destination）；
每个 `await` 后检查归属；已经派发的任务属于原会话，**不能自动改投、取消或重发**；迟到结果
可更新原目标的受限状态缓存，但不得改新会话 DOM、清空新草稿。

请求守卫由 `fragment-requests.ts` 落实：`beforeRequest` 绑定作用域快照 → **`beforeOnLoad`
检查归属并 `preventDefault` 过期响应**（该钩子早于 HX-Trigger/重定向处理）→ `beforeSwap` 再核对
目标节点与 OOB 内容。在 Promise 完成或 `afterSwap` 后再检查已经太晚；abort 只是节流，不能替代守卫。

队列：本条消息的 destination（`steering`/`followUp`）与队列投递模式（`all`/`one-at-a-time`）
是两件事，不能互相推导。发送时先同步 `session.set_queue_mode`，再带 `streamingBehavior`
（取值为 Pi 的 `steer`/`followUp`——**队列配置叫 `steering`，prompt 参数叫 `steer`**）。

模型：历史页的 `historicalModel` 只标记磁盘分支上的历史选择；当前模型以 worker 的
`session.state.model` 为准，`null` 或 `unknown/unknown` 不能作为发送目标。新会话的模型/思考选择
作为**用户意图**单独保留，启动回读不覆盖。

事件：epoch 只由当前连接的订阅确认建立，旧 epoch/旧连接的事件一律忽略；`omitted`/`resync`
立即标记 live 缺口并重读持久历史；`message_end` 是权威内容，`agent_settled` 后对账持久投影。

## 测试与验证

```bash
pnpm test         # Vitest（jsdom），22 个文件约 2 秒
pnpm typecheck    # tsc --noEmit
pnpm build        # 产物
pnpm check        # 契约 + 对比度
```

写等待时注意 `vi.waitFor` **默认 50ms 轮询一次**：本仓判断的都是同步 DOM 或
调用状态，几十处等待累积起来就是几秒的虚耗（实测 workbench 那一处 6.9 秒 → 2.1 秒，
全部来自把轮询间隔收到 1ms）。文件内的辅助 `waitFor` 已经这么定义，新增用例直接用它。

| 层 | 负责 |
|---|---|
| Go 渲染测试 | 真实模板字段、转义、片段形状、边界投影 |
| TypeScript / Vitest | 目标捕获、迟到响应、用户意图、队列语义、附件预算、dispose |
| 契约检查 | manifest、模板结构、产物引用、gzip 预算、主题归属 |
| 浏览器（真实桥 + 假 Pi） | 真实 htmx 交换顺序、HX 响应副作用、滚动、富内容、移动布局、云端闭环 |

**任何一层都不能替其他层背书**；测试通过不意味着新边界已覆盖，新修复必须把反例转为正式测试。

改动 UI 后至少跑 `pnpm test && pnpm build && pnpm check`；动了模板或片段端点，再补
`PI_WEBUI_DIR=../pi-webui-htmx go test -race ./...`（桥侧跨目录契约测试）。

## 常见坑

- **`hx-trigger="load"` 在认证完成前触发**：请求 401 后 htmx 不会重试，片段永远停在占位文字。
  片段端点的加载一律由显式事件驱动（如 `files-refresh`），不要用 `load`。
- **浏览器内所有路径必须是相对路径**：模板 `hx-*`/`href`（契约脚本会拦绝对路径）、
  前端用 `src/lib/url.ts` 解析、`pushState` 保留 `location.pathname`。
  这样同一份产物适配本机、反代与 relay 设备前缀三种形态。
- **`hx-disabled-elt` 对 `div` 无效**（`div` 不响应 `disabled`）；要防重复提交用 `hx-sync="this:drop"`。
- **思考/工具块懒加载**：正文由按钮 `hx-get` 拉取（`data-lazy="thinking"`），不要在前端缓存正文；
  Blob URL 与挂载作用域一起释放。
- **首屏预算是硬约束**：新增依赖前先看 `ui-manifest.json` 的 `budgetNote`。
- **供应商分块**：htmx 由 `manualChunks` 单独成块并在 shell 里 `modulepreload`，
  不要把它并入入口 chunk，否则改自有代码会顶掉它的缓存。
