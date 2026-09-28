# 组件选型、工具链与资源约束

更新：2026-09-27。依赖版本以 `package.json` 和 `pnpm-lock.yaml` 为准。本页纠正旧 vendor/无构建/无测试描述，不升级依赖。[交互契约](contract.md) 区分当前实现与目标修复。

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
| Tailwind / Vite 插件 | ^4.3.3 | 扫描 Go 模板的工具类，与 CSS 设计令牌配合 |
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

Vite 处理 JS/CSS；Tailwind 扫描 `src/templates`；Go 在运行时加载模板。桥通过 `dist/.vite/manifest.json` 解析哈希资源。发布时模板、manifest 和 dist 必须是同一构建，重建 UI 后重启桥。

- 首屏预算拆为两个都强制执行的数字：总预算 `build.firstLoadBudgetGzipKB`（**42 KiB gzip**）与自有代码预算 `build.firstLoadOwnBudgetGzipKB`（**24 KiB gzip**）。`build.vendorChunks` 声明哪些分块算供应商代码。
- 统计入口的全部静态依赖闭包；动态内容库不计入初始入口预算，但在第一次使用时仍真实消耗网络/内存。
- 2026-09-28 实测构成：自有代码 **22.65 KiB**（JS 15.66 + CSS 7.00）+ 供应商 htmx **17.59 KiB** = **40.24 KiB**。
- htmx 体积已对着包核实（2.0.11）：官方 `dist/htmx.min.js` 为 52,182 B / gzip 16,861 B；npm 包的 `main` 指向未压缩的 `dist/htmx.esm.js`（171,382 B），所以打包后 gzip 17.59 KiB，比官方压缩版多约 0.73 KiB（Vite 的压缩略弱于官方 terser 产物）。htmx 本身不是胖库，这一块属于换不掉的固定成本，因此单列。
- htmx 由 `vite.config.ts` 的 `manualChunks` 单独成块，桥在 shell 里为它输出 `modulepreload`：拆分不会多一个往返，同时我们改自己的代码不会顶掉它的缓存。
- 历史测量“页面+首次数据 brotli 34.9 KB”属于另一构建/资源集合，不能拿来当本次首屏新测量。也不把旧 Pi Web 资源数字当公平的持续性能对照。
- 不能为通过预算而只改数字；先检查静态依赖误入首屏、重复模块和不必要初始化，再决定范围。本轮核查过一次全量 CSS（154 个 class/id 选择器）没有真正的死代码：未在源码里直接出现的 `.toast-*`、`.state-*`、`.diff-*` 分别是模板字符串、条件拼接和桥的 Go 模板生成的类名。

## 3.1 职责边界：谁负责生成 HTML

htmx 侧重 HTML 与后端，因此边界按「数据 → HTML 归桥，瞬时交互留浏览器」划：

**归桥（服务端渲染片段 + `hx-*` 属性）**——凡是「把已有数据结构排版成 HTML」都属此类。
片段端点：`/ui/sessions`、`/ui/search`、`/ui/sessions/{id}/history`、`/ui/models`、`/ui/packages`、
`/ui/files`、`/ui/git-status`、`/ui/diff`、`/ui/branch`、`/ui/extensions/*`。
刷新一律走「隐藏输入带参数 + `hx-trigger` 自定义事件 + `hx-include`」，
前端不再拼 URL、不再用 `createElement` 搭列表。

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

**片段端点的状态机约定**：htmx 换入的片段端点一律返回 200 + 可读 HTML，包括「worker 未启动」这类前置状态。原因是 htmx 默认不交换 4xx/5xx，按错误码返回会让面板停在旧内容上且没有解释（真机复现过）。由服务端渲染状态（`RenderNote`）是 htmx 的用法本意。`/ui/file-text`、`/ui/file-image`、lazy 加载、`/ui/exports/*` 不是 htmx 交换目标，保留真实状态码。`branch.ts` 从 7.1 KB / 11 处 DOM 降到 3.4 KB / 1 处。

**同一条判据下还剩这些候选**（尚未迁）：`topbar.ts` 的「会话信息 / 系统」事实表（来自 `session.state` 与 `session.stats`）、
`models.ts` 的「发现模型」结果列表（来自 `config.models.discover`）、`workbench.ts` 的思考等级下拉与工作目录 datalist、
`workspace.ts` 的文件预览容器。`workbench.ts` 里的扩展 widget 与附件缩略图**不迁**——前者是 WS 推送的活跃状态，
后者是尚未上传的本地 `File`，服务端没有权威版本。

**留浏览器**——只有转瞬即逝的交互状态，没有服务端等价物：
按键驱动的补全与斜杠菜单、滚动锚定、WS 流式增量、textarea 自适应高度、
xterm 终端、未上传的本地附件缩略图、markdown/高亮/KaTeX/ANSI 渲染管线、toast 通知。
判据是「这份数据在服务端有没有权威版本」：有就该渲染成 HTML 传下来。

这条边界是 2026-09-28 复核时收紧的：`git-status`、`search`、`branch` 三处原先在前端
用 `createElement` 重建，与桥的模板重复，且截断文案、层级缩进这类规则要维护两份。

一处刻意的例外：`/ui/file-text` 用 `fetch` 而不是 `hx-get`——它返回的是文件**内容**
（可能很大），直接交给渲染管线（高亮/ANSI）而不是交换进 DOM。

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

当前 `tests/unit` 有 11 个文件、72 项基线测试。TypeScript 管类型，Go 管模板/投影，契约脚本管结构与产物，Vitest 管异步/状态/生命周期，真实浏览器管交换/滚动/富内容行为。

新修复优先补：每个 await 点切换目标、beforeOnLoad 阻止旧响应副作用、并发附件预留、保存时继续编辑、深树迭代、组件重复挂载/卸载。基线绿不能替代这些反例；两仓独立 CI 已配置，本地联测通过；托管运行待接入远程，缺少配套 UI 的独立桥测试不算跨仓验收。
