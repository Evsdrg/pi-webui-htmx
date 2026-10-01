# 开发说明（前端）

> 面向使用者的安装与启动说明见[桥的 README](../../pi-bridge-go/README.md)；设计取舍见
> [组件选型](components.md) 与 [UI 包契约](contract.md)。本文件是前端的开发细节与已知坑。

## 目录结构

```text
src/entry/app.ts     入口：装配各模块、按需运行增强
src/modules/         工作台与交互模块（见下表）
src/templates/       Go 模板；桥在运行期渲染，浏览器只接收片段
src/styles/          tokens.css / app.css / code.css 三表（见下）
src/types/           protocol.ts（v1 协议类型）、htmx.d.ts
src/lib/             url.ts（basePath 自适应）、hljs.ts（高亮按需加载）
scripts/             契约与对比度检查
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

## 开发循环

```bash
pnpm install --frozen-lockfile
pnpm dev          # vite build --watch，改 src 后自动重建 dist/
# 另一个终端：桥指向本目录
cd ../pi-bridge-go && go run ./cmd/pi-bridge --ui-dir ../pi-webui-htmx ...
```

⚠️ **重建 `dist/` 之后必须重启桥**：桥在启动时读取 `ui-manifest.json` 并缓存入口资源名，
不重启会继续引用旧哈希的 assets（页面刷新无效）。这是最容易浪费时间的坑。

## 构建与产物

`pnpm build` = `tsc --noEmit` + `vite build`。产物里的三份清单必须与桥对齐：

| 文件 | 内容 |
|---|---|
| `ui-manifest.json` | 协议版本、必需方法、首屏预算、入口资源名 |
| `dist/.vite/manifest.json` | Vite 的 chunk 图 |
| `dist/assets/*` | 带内容哈希的实际资源 |

桥启动时校验 `protocolVersion` 与 `requiredMethods`，不匹配就明确失败（没有内嵌模板或 CDN 回退）。

## 样式：三张表，职责严格分离

| 文件 | 职责 | 约束 |
|---|---|---|
| `src/styles/tokens.css` | 5 套主题的调色板与派生令牌 | **主题色值的唯一来源**；深色系有两个入口（`[data-theme=dark]` 与 `prefers-color-scheme`），必须引用同一组别名 |
| `src/styles/app.css` | 组件样式，分节组织 | 不写任何主题色值；末尾是响应式与无障碍 |
| `src/styles/code.css` | highlight.js 配色 | 按需加载，不进首屏；颜色全部走 `var(--code-*)` |

两条规则由 `scripts/check-contract.mjs` 强制执行：主题色值只允许出现在 tokens.css；
`.hljs` 背景必须 `transparent`。改主题时只动 tokens.css。

Tailwind 只用于 preflight 复位——模板里**没有**工具类，因此 `app.css` 不写 `@source`，
样式一律手写。

## 职责边界

**数据 → HTML 由桥渲染**；TypeScript 只做浏览器独有的交互增强。

- 需要新片段时：加模板 + 桥侧渲染函数 + `hx-*` 属性；不要用 TS 拼 DOM。
- 例外（明确保留在浏览器）：剪贴板、拖拽、草稿与偏好持久化、流式增量渲染、
  富内容增强（markdown/高亮/公式/图表）、终端与图片预览的生命周期。
- 新增接口要先与桥协商并配套发布（参见 [UI 包契约](contract.md) 的协议版本一节）。

## 测试与验证

```bash
pnpm test         # Vitest（jsdom），tests/unit 下 22 个文件
pnpm typecheck    # tsc --noEmit
pnpm build        # 产物
pnpm check        # 契约：相对路径、主题归属、预算、模板字段；对比度：5 主题 × 6 文字色 × 2 底色
```

分层原则：Go 侧测模板数据与渲染，TypeScript 测类型与状态机，契约脚本测产物结构与预算，
Vitest 测异步/作用域/守卫，浏览器验收（真实桥 + 假 Pi）测真实交换与滚动。**任何一层都不替其他层背书。**

改动 UI 后至少要跑：`pnpm test && pnpm build && pnpm check`；动了模板或片段端点，
再补 `PI_WEBUI_DIR=../pi-webui-htmx go test -race ./...`（桥侧跨目录契约测试）。

## 常见坑

- **`hx-trigger="load"` 在认证完成前触发**：请求 401 后 htmx 不会重试，片段永远停在占位文字。
  片段端点的加载一律由显式事件驱动（如 `files-refresh`），不要用 `load`。
- **浏览器内所有路径必须是相对路径**：模板 `hx-*`/`href`（契约脚本会拦绝对路径）、
  前端用 `src/lib/url.ts` 解析、`pushState` 保留 `location.pathname`。
  这样同一份产物适配本机、反代与 relay 设备前缀三种形态。
- **思考/工具块懒加载**：正文由按钮 `hx-get` 拉取（`data-lazy="thinking"`），
  不要在前端缓存正文；Blob URL 与挂载作用域一起释放。
- **首屏预算是硬约束**：自有代码 30 KiB / 总 50 KiB（gzip）。新增依赖前先看
  `ui-manifest.json` 的 `budgetNote`；KaTeX、Mermaid、xterm 都必须按需动态导入。
- **供应商分块**：htmx 由 `manualChunks` 单独成块并在 shell 里 `modulepreload`，
  改自有代码不会顶掉它的缓存——不要把 htmx 并入入口 chunk。
