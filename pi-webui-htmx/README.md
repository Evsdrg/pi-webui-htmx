# pi-webui-htmx

> **面向使用者的说明（安装、启动、配置）见桥仓的 [README](../pi-bridge-go/README.md)。**
> 本文件是前端的开发说明。

Pi Bridge 的 HTMX 工作台。模板、TypeScript 与样式归本仓；桥负责数据、进程及受控接口。目标是接近 Pi Web 的工作台体验，保持较小首屏和独立 Pi 进程。

**目录：** 本目录是 HTMX/TypeScript 前端与 UI 包；相邻的 `../pi-bridge-go` 是 Go 桥，二者同属一个仓库。跨目录改动分别提交并同时通过，配套关系见 [UI 包契约](docs/contract.md)。上游 Pi Web 仅作对照实现，不是本项目的依赖。

**状态：** 本地与云端（relay 设备前缀）两条链路都已端到端跑通。云端形态下外壳、片段、资源与 WS 全部经设备前缀转发，前端因路径全部相对化而无需知道自己跑在哪种形态。**未验收**：真实模型的长时流式、公网跨机 RTT 与丢包、小时级长稳。

## 文档

- [开发说明](docs/DEVELOPMENT.md)：目录结构、开发循环、样式规则、测试与常见坑。
- [UI 包与交互契约](docs/contract.md)：模板字段、版本、构建与交换守卫。
- [组件选型](docs/components.md)：依赖、样式组织、资源释放与职责边界。
- [桥的架构](../pi-bridge-go/docs/architecture.md)：S01–S12、取舍与边界约束。
- [v1 协议](../pi-bridge-go/api/v1/protocol.md)：可调用的方法、事件与限额。
- [通信约定](../pi-bridge-go/docs/communication.md)：分层、受理、订阅与背压。

## 安装、构建和测试

```bash
pnpm install --frozen-lockfile
pnpm test
pnpm typecheck
pnpm build
pnpm check
```

当前工具链：pnpm 11.22、Vite 8、TypeScript 7、Tailwind 4、Vitest 5；精确安装版本以锁文件为准。Vite 构建 JS/CSS，Go 在运行时渲染模板；Tailwind 只用于 preflight 复位（模板里没有工具类，因此不写 `@source`），组件样式是手写 CSS。

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
| ✅ 已完成 | 模型配置：两级树 + 字段表单 + 目录接线（catalog），JSON 源码保留为逃生门 |
| ✅ 已完成 | 跨会话附件/命令/迟到响应、首次模型选择、队列建模、预览清理、深树与限额 |
| ✅ 已完成 | 历史树与导出改为磁盘投影，**不再需要 worker**；普通列表/历史本来就不启动 Pi |
| ✅ 已完成 | 云端经 relay 设备前缀的完整 HTTP+WS 工作台（同一份产物相对路径自适应） |

包清单只读，不做安装/更新。OAuth/额度查询明确排除；Web Push 暂缓。PWA、版本检查、PDF、Minimap、多语言等未立项，不推断为用户明确不要。

## 交互约定（已落地）

1. 用 SessionScope 固定 session/draft、generation 和资源所有权；命令一开始捕获目标，每个 await 后检查，不能读取新的全局 sessionId 改投。
2. htmx 在 beforeOnLoad 拦截旧响应，在 beforeSwap 复核目标；旧请求的 HX-Trigger/重定向也不能影响新页面。仅 afterSwap 检查无效。
3. 区分用户意图和服务端状态；启动回读不覆盖首次模型选择，保存回执不覆盖等待期间的新草稿。
4. 排队 destination 与两条队列的投递 mode 独立；自动重试无 Pi 读回时显示未知或本 worker 本地确认值。
5. 附件按 draft 隔离、异步前预留预算；目标走受限 HTTP 上传引用，并在 Pi base64/RPC 层再次校验完整字节。
6. 连接确认决定 epoch；omitted/resync 立即重新同步并保留生成中缺口。文本/thinking 使用有界缓冲和 rAF 批量更新。
7. 图片 URL、xterm、Observer、监听器和 timer 归组件 dispose；切换/替换释放，断线不谎称远端终端已经关闭。

保留 HTMX+TypeScript，不通过更换框架解决作用域/顺序问题。新接口和模板字段必须与桥协商并配套发布。

## 体积与渲染

首屏预算是硬约束，两个数字都在 `ui-manifest.json` 的 `build` 里：总 **50 KiB**、自有代码 **30 KiB**（均为 gzip）。`pnpm check` 递归统计入口的静态依赖闭包，并按 `vendorChunks` 把供应商分块单列——htmx 是一块换不掉的固定成本（gzip 17.59 KiB），单列才能让「改 UI」只盯自己的额度。当前构成：自有 28.78 + htmx 17.59 = 46.37 KiB。

KaTeX、Mermaid、xterm 等按需加载，不计入首屏。`dist/` 不入库，改动后需重新构建并重启桥。

历史优先按回合分页但遵守硬限额；滚动由前端用户位置决定，X-Scroll-Mode 只提示。Markdown 必须净化；模板保持 Go 转义。入口已关闭 htmx eval/script 标签处理，不能因此取消其他安全层。

## 验收基线

22 个 Vitest 文件共 184 项通过，typecheck、build、check 与对比度核算通过。

隔离浏览器验收使用真实 Go 桥 + 假 Pi，覆盖普通发送/分页、扩展确认、富内容、终端关闭、迟到的响应、并发附件、三视口布局，不调用付费模型。

Go 测模板/数据，TypeScript 测类型，契约脚本测产物结构，Vitest 测状态/异步，浏览器测真实交换/滚动；任何一层都不能替其他层背书。
