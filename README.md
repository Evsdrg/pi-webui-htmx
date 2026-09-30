# pi-webui-htmx

Pi Bridge 的 HTMX 工作台。模板、TypeScript 与样式归本仓；桥负责数据、进程及受控接口。目标是接近 Pi Web 的工作台体验，保持较小首屏和独立 Pi 进程。

**仓库边界：** 本仓只含 HTMX/TypeScript 前端与 UI 包。相邻的 `../pi-bridge-go`（Go 桥）与 `../pi-web`（上游 Pi Web 参考）各自是独立 git 仓库；三者没有共同父仓库，也不要为它们建一个总仓库。跨仓改动分两边提交，配套关系见 [UI 包契约](docs/contract.md)。

**状态（2026-09-27）：** 本地主要链路已接线；完整审查发现的并发/切会话/传输等问题尚未修复。P0 的 CI 配置与两仓本地联测已完成；产品修复仍按整体规划推进。云端 relay UI 仍未接通。

## 文档

- [UI 包与交互契约](docs/contract.md)：当前模板字段、版本、构建与目标 SessionScope/交换守卫。
- [组件选型](docs/components.md)：依赖、样式组织、资源释放与当前职责边界。
- [前端改动复核](docs/htmx-css-ts-review.md)：F01–F19 的历史证据与跨仓落地验收。
- [跨仓实施方案](docs/frontend-repair-plan.md)：草稿、请求归属、服务端片段与验证顺序。
- [整体实施规划](../pi-bridge-go/docs/repair-plan.md)：P0–P7、影响矩阵、作用域边界、配套发布与状态迁移。
- [桥的架构与修复决策](../pi-bridge-go/docs/architecture.md)：S01–S12、技术取舍和验收条件。
- [审查台账](../pi-bridge-go/docs/code-audit.md)：未解决问题及逐项方案归属。
- [v1 协议](../pi-bridge-go/api/v1/protocol.md)：当前可调用的方法，不把拟议能力当现成接口。

## 安装、构建和测试

```bash
pnpm install --frozen-lockfile
pnpm test
pnpm typecheck
pnpm build
pnpm check
```

当前工具链：pnpm 11.22、Vite 8、TypeScript 7、Tailwind 4、Vitest 5；精确安装版本以锁文件为准。Vite 构建 JS/CSS，Go 在运行时渲染模板，Tailwind 通过 @source 扫描模板类名。

在相邻桥目录启动时指定 `--ui-dir ../pi-webui-htmx`。模板、ui-manifest 和 dist 必须是同一构建；桥在启动时快照资源，重新 build 后重启桥。没有内嵌模板/CDN 缺库回退。

## 实际布局

```text
src/entry/app.ts   入口
src/modules/      工作台、WS、异步状态与惰性模块
src/templates/    Go 模板；extensions/ 为通用扩展通道
src/styles/       CSS 设计令牌与 Tailwind
src/types/        v1 协议与 htmx 类型
src/lib/          加载/打包边界辅助
tests/unit/       Vitest 行为测试
tests/fixtures/   可控 RPC 假 Pi
dist/             生成资产与 Vite manifest，不提交
```

## 能力与差距

| 状态 | 范围 |
|---|---|
| ✅ 本地已有 | 三栏布局、主题、会话列表/历史、流式对话、模型/思考选择、排队与停止 |
| ✅ 本地已有 | 分支/fork、文件/Git、PTY、图片附件、Markdown/代码/公式/图表、通用扩展 UI |
| ⚠️ 部分完成 | 模型配置是 JSON 文本编辑，discover/test 已有，catalog 与字段表单未接 |
| ⚠️ 待修 | 跨会话附件/命令/迟到响应、首次模型选择、队列建模、预览清理、深树与限额 |
| ⚠️ 依赖 worker | 历史树与导出；普通列表/历史不启动 Pi |
| ❌ 未接通 | 云端通过 relay 的完整 HTTP+WS 工作台 |

包清单只读，不做安装/更新。OAuth/额度查询明确排除；Web Push 暂缓。PWA、版本检查、PDF、Minimap、多语言等未立项，不推断为用户明确不要。

## 本轮确定的交互修复约定（待实现）

1. 用 SessionScope 固定 session/draft、generation 和资源所有权；命令一开始捕获目标，每个 await 后检查，不能读取新的全局 sessionId 改投。
2. htmx 在 beforeOnLoad 拦截旧响应，在 beforeSwap 复核目标；旧请求的 HX-Trigger/重定向也不能影响新页面。仅 afterSwap 检查无效。
3. 区分用户意图和服务端状态；启动回读不覆盖首次模型选择，保存回执不覆盖等待期间的新草稿。
4. 排队 destination 与两条队列的投递 mode 独立；自动重试无 Pi 读回时显示未知或本 worker 本地确认值。
5. 附件按 draft 隔离、异步前预留预算；目标走受限 HTTP 上传引用，并在 Pi base64/RPC 层再次校验完整字节。
6. 连接确认决定 epoch；omitted/resync 立即重新同步并保留生成中缺口。文本/thinking 使用有界缓冲和 rAF 批量更新。
7. 图片 URL、xterm、Observer、监听器和 timer 归组件 dispose；切换/替换释放，断线不谎称远端终端已经关闭。

保留 HTMX+TypeScript，不通过更换框架解决作用域/顺序问题。新接口和模板字段必须与桥协商并配套发布。

## 体积与渲染

首屏预算拆成两个数字，都存在 ui-manifest 的 build 里：总预算 `firstLoadBudgetGzipKB`（当前 **50 KiB gzip**）与自有代码预算 `firstLoadOwnBudgetGzipKB`（当前 **30 KiB gzip**）。check 递归统计入口静态依赖闭包，并按 `vendorChunks` 把供应商分块单列。KaTeX、Mermaid、xterm 等按需加载。

需要把供应商和自己写的分开，是因为 htmx 是一块**换不掉的固定成本**：官方 `dist/htmx.min.js`（2.0.11）为 52,182 B / gzip 16,861 B，而 npm 包的 `main` 指向未压缩的 `dist/htmx.esm.js`（171,382 B），因此打包后是 gzip 17.59 KiB，比官方压缩版多约 0.73 KiB。把它混进同一个数字里，等于每次改 UI 都在和别人的体积抢额度。

2026-09-30 实测构成：自有代码 28.19 KiB + 供应商（htmx）17.59 KiB = 45.77 KiB。htmx 由 vite.config.ts 的 `manualChunks` 单独成块，桥在 shell 里为它输出 `modulepreload`，因此拆分不会多一个往返，同时我们改自己的代码不会顶掉它的缓存。

历史优先按回合分页但遵守硬限额；滚动由前端用户位置决定，X-Scroll-Mode 只提示。Markdown 必须净化；模板保持 Go 转义。入口已关闭 htmx eval/script 标签处理，不能因此取消其他安全层。

## 验收基线

2026-09-30：22 个 Vitest 文件共 165 项通过，typecheck、build、check、对比度核算通过。审查反例已按 F01–F19 逐条补上判定性测试，见 [docs/htmx-css-ts-review.md](docs/htmx-css-ts-review.md)。

既有隔离浏览器验收使用真实 Go 桥+假 Pi，覆盖普通发送/分页、扩展确认、富内容、终端关闭、移动布局等，不调用付费模型。新方案还需验证迟到响应、每个 await 的切换、并发附件、重连/大文件和云模式。

Go 测模板/数据，TypeScript 测类型，契约脚本测产物结构，Vitest 测状态/异步，浏览器测真实交换/滚动；任何一层都不能替其他层背书。
