# 组件选型、工具链与资源约束

依赖版本以 `package.json` 与 `pnpm-lock.yaml` 为准；[交互契约](contract.md) 约定模板字段与交换守卫；
开发细节与常见坑见 [DEVELOPMENT.md](DEVELOPMENT.md)。

## 1. 技术栈与依赖

| 组件 | 声明版本 | 职责与约束 |
|---|---|---|
| htmx | ^2.0.11 | HTML 片段请求/替换；ESM 由 Vite 构建，入口显式挂 `window.htmx` |
| marked + DOMPurify | ^18 / ^3.4 | Markdown 解析后净化；不信任模型输出，不直接插入 marked 原始结果 |
| highlight.js | ^11.12 | 按扩展名/代码标签提示语言，不用自动误判；语言包由 `lib/hljs` 控制 |
| KaTeX | ^0.18 | 公式按需加载（检测到分隔符才拉） |
| Mermaid | ^12 | 动态 import，图表出现才加载；不以裸包名绕过构建（传递依赖含 EPL-2.0，见根 README） |
| xterm + FitAddon | 6.0 / 0.11 | 有状态 PTY 字节流；动态加载，关闭/断线/dispose 语义分开处理 |
| ansi_up | ^6.0.6 | ANSI 转义输出；ANSI 内容不再交给 hljs 二次处理 |
| Tailwind | ^4.3 | **只用于 preflight 复位**——模板里没有工具类，样式是手写 CSS |
| Vite / TypeScript | ^8.3 / ^7.0 | 代码分割、哈希与严格类型检查；不编译 Go 模板 |
| Vitest / jsdom | 5.0.2 / 30.1.1 | 模块行为测试，补充 Go 侧模板测试 |
| pnpm | 11.22.0 | 锁文件安装与脚本入口；可复现安装用 `--frozen-lockfile` |

## 2. 为什么不更换框架

要解决的问题是目标归属、交换时序、资源生命周期与跨层预算——换 React/Vue、改 SSE 或引入
状态库都不会自动修复这些。保留 HTMX + 小型 TypeScript 模块，用 SessionScope、请求序号与
disposer 显式管理状态。

| 选择 | 原因 |
|---|---|
| 不引入 UI/状态管理框架 | 模板与模块已能覆盖布局；缺的是显式状态所有权 |
| 继续服务端渲染 | 桥已有数据与 Go 模板；避免再维护一套渲染器 |
| 小型 SVG 图标 + CSS 变量 | 五套主题复用，不为按钮引入组件体系 |
| 先分页，不上虚拟滚动 | 保留稳定的回合/entry 锚点；超长回合受硬限额约束 |
| 不先引 i18n 框架 | 产品范围未立项，不用「刻意不要」替代真实决定 |

「DOM 即全部状态」不成立：会话/草稿、用户意图、请求代次、服务端回执、组件资源都需要小型
类型化状态；**DOM 只是显示投影**。

## 3. 构建与首屏预算

```bash
pnpm install --frozen-lockfile
pnpm test && pnpm typecheck && pnpm build && pnpm check
```

Vite 处理 JS/CSS，Tailwind 提供复位，Go 在运行时加载模板；桥通过 `dist/.vite/manifest.json`
解析哈希资源。**模板、manifest 与 dist 必须来自同一构建**，重建后重启桥。

预算拆成两个强制执行（都在 `ui-manifest.json` 的 `build` 里）：

| 数字 | 当前值 | 说明 |
|---|---|---|
| `firstLoadOwnBudgetGzipKB` | 30 KiB | 自有代码；改 UI 时该盯的数字 |
| `firstLoadBudgetGzipKB` | 50 KiB | 含供应商；`vendorChunks` 声明哪些分块算供应商 |

当前构成（gzip）：自有 28.78 KiB + htmx 17.59 KiB = 46.37 KiB。htmx 是**换不掉的固定成本**
（npm 包的 `main` 指向未压缩的 `dist/htmx.esm.js`，比官方压缩版多约 0.73 KiB），因此单列成块：
`vite.config.ts` 的 `manualChunks` 让它独立，桥在 shell 里输出 `modulepreload`——拆分不多一个
往返，且改自有代码不会顶掉它的缓存。

统计的是入口的**静态依赖闭包**；动态内容库（KaTeX/Mermaid/xterm）不计入首屏，但首次使用时
仍真实消耗网络与内存。

## 4. 职责边界：谁负责生成 HTML

判据是「这份数据在服务端有没有权威版本」——有就渲染成 HTML 传下来。

**归桥**（服务端片段 + `hx-*` 属性）：凡是「把已有数据排版成 HTML」都属此类。

- 片段端点：`/ui/sessions`、`/ui/search`、`/ui/sessions/{id}/history`、`/ui/models`、`/ui/packages`、
  `/ui/files`、`/ui/git-status`、`/ui/diff`、`/ui/branch`、`/ui/extensions/*`、`/ui/dirs`、`/ui/mc`、
  `/ui/mc/content`、`/ui/system`、`/ui/tools`、`/ui/stats`
- 模型发现/连通测试用认证 POST（`/ui/models/discover`、`/ui/models/test`），结果由桥渲染，**凭据不进 URL**
- 思考正文走 lazy 路径的 `format=html` 变体
- 模板声明动作、目标与同步域；需要 JS 参与时用隐藏输入或 `htmx.ajax`，不在 TS 里生成结果列表

**片段端点的状态码约定**：htmx 换入的片段**一律 200 + 可读 HTML**，包括「worker 未启动」这类
前置状态——htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容且没有解释（真机复现过）。
由服务端渲染状态说明（`RenderNote`）是 htmx 的用法本意。

保留真实状态码的例外：`/ui/file-text`、`/ui/file-image`、lazy 正文（含 `format=html`）、
`/ui/exports/*`——这些不是「片段交换」而是内容/产物读取，前端按 `response.ok` 分支。

**留浏览器**（只有瞬时交互状态，服务端没有权威版本）：按键驱动的补全与斜杠菜单、滚动锚定、
WS 流式增量、textarea 自适应、xterm 终端、未上传的本地附件缩略图、富内容渲染管线
（markdown/高亮/KaTeX/ANSI/mermaid）、toast。

一处刻意例外：`/ui/file-text` 用 `fetch` 而不是 `hx-get`——它返回文件**内容**（可能很大），
交给渲染管线而不是交换进 DOM。

**已对齐 Pi Web 的呈现**：顶栏三面板（系统/工具/会话）的结构与分组、无面板标题行（靠再次
点击工具栏按钮或 Escape 关闭）、模型选择器只显示模型名（provider 进 `title`）、数字格式
（千位分隔、`formatCompact` 的 `k`/`M`、百分比一位小数、费用四位小数）。

**唯一未移植**：「活跃时长」——`get_session_stats` 不含它，桥要给出就得整份扫 JSONL，
与「打开面板时不重扫历史」冲突；补齐应改为在索引构建时累加。

## 5. 样式组织

三张表，职责不重叠：

| 文件 | 内容 | 加载 |
|---|---|---|
| `src/styles/tokens.css` | **主题的唯一来源**：5 套调色板、派生令牌、语义色 | 首屏 |
| `src/styles/app.css` | 组件样式，分节；末尾是响应式与无障碍 | 首屏 |
| `src/styles/code.css` | highlight.js 配色 | 按需（由 `lib/hljs` 引入） |

以下不变量由 `scripts/check-contract.mjs` 强制（改坏会直接失败）：

1. **主题色值只允许出现在 `tokens.css`**；`app.css` 出现十六进制色值即失败（与主题无关的叠加层除外）。
2. 深色主题两个入口（`[data-theme=dark]` 与 `prefers-color-scheme`）的令牌集合必须**完全一致**。
3. `.hljs` 背景必须 `transparent`；`code.css` 颜色全部走 `var(--code-*)`，且不得引入 highlight.js 自带主题
   （它在浅色主题下会变成「浅框套深块」）。
4. 会话详情的列模板**只能由分组规则**声明（`.stats-info/.stats-message/.stats-token .stats-rows`）；
   通用 `.stats-rows` 不得声明 `grid-template-columns`——特异性反转让 Token 数值被推到面板最右。
5. Token 组必须是 `max-content max-content` + `justify-content: start`（Pi Web 的 compact 形态）。
6. 顶栏里**带 `hidden` 的元素不得靠 `margin-left:auto` 定位**；右对齐由 `.topbar-right` 容器承担。
7. 模板里不得出现根绝对路径（`hx-*`/`href`/`src`）；不得含动态内联 `style`（CSP）。
8. 自定义属性被引用前必须已定义。

对比度由 `scripts/check-contrast.mjs` 校验：5 个主题 × 6 个文字色 × 2 种底色逐对算比值，
低于 4.5:1 直接失败。

## 6. 安全与 CSP

入口关闭 htmx 的 eval/script 标签执行与 history cache；Go 模板转义与 DOMPurify 净化另行负责
（构建器对 htmx 内部 eval 的警告不等于应用已走该路径，不能因此取消内容净化）。

- 模板缩进类布局不得用内联 `style`（片段受 CSP 约束，契约检查拦截）；分支树层级用
  `aria-level` + 静态 CSS 表达（上限 11 级，与 `internal/presentation` 的 `branchMaxLevel` 一致）。
- 目标 CSP：同源脚本；blob/data 图片仅限批准的图片路径；字体同源；连接限制到实际服务来源，
  不泛放 ws/wss。KaTeX、Mermaid、xterm 的运行时样式需要单独核验。
- 文件预览、Markdown、图表、ANSI 各走独立处理路径；「已净化」不构成忽略 URL/属性/资源权限的理由。

## 7. 资源所有权

| 资源 | 所有者 | 释放时机 |
|---|---|---|
| fetch / htmx 读取 | 设备或会话视图 + 面板序号 | 目标切换时 abort；响应处理前仍检查归属；切聊天不取消全局配置草稿 |
| 图片 blob URL | 预览组件 | 替换、片段清理、离开会话时 revoke |
| xterm、ResizeObserver、输入定时器 | 终端面板 | 确认关闭后 dispose；断线只禁输入，不谎称服务端已结束 |
| 附件异步读取与配额 | 草稿/会话 | 读前预留，成功转交，取消/失败释放 |
| live 文本与 thinking 缓冲 | 当前订阅代次 | rAF 批量输出；settled/重同步/切换时释放 |

htmx 片段替换同样触发清理，不只依赖整页卸载。

## 8. 测试分层

TypeScript 管类型，Go 管模板与投影，契约脚本管产物结构与预算，Vitest 管异步/状态/生命周期，
真实浏览器（真实桥 + 假 Pi）管交换/滚动/富内容。**任何一层都不能替其他层背书。**

新增交互优先补的反例：每个 await 点切换目标、`beforeOnLoad` 阻止旧响应副作用、并发附件预留、
保存过程中继续编辑、深树迭代、组件重复挂载/卸载。
