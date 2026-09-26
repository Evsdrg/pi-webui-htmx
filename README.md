# pi-webui-htmx

Pi Bridge 的 htmx 前端。模板与静态资源归本仓，桥只提供数据。

**契约见 [`docs/contract.md`](docs/contract.md)。** 改模板样子不需要动桥；
改数据字段名、URL 或协议版本才需要。

## 工具链

pnpm + Vite 8 + TypeScript 7 + Tailwind v4。

用户的要求是「工具链可以重，最终产物足够轻」，因此选了完整栈，
产物侧由契约强制约束：

```bash
pnpm install
pnpm typecheck   # tsc --noEmit，协议类型必须与桥一致
pnpm build       # 产出 dist/（带内容哈希 + Vite manifest）
pnpm check       # 契约校验，含首屏体积预算
```

**Vite 只处理 JS/CSS，不处理 Go 模板。** 模板由桥在请求时渲染。
Tailwind 通过 `@source` 扫描 `src/templates/`，才能产出用到的类。

## 结构

```
src/
├── templates/      # 服务器渲染片段，htmx 直接换入 DOM
│   └── extensions/ # 通用扩展通道三件套（不针对任何具体插件）
├── entry/app.ts    # 唯一入口，只放首屏必需的部分
├── modules/        # 流式层与各惰性渲染模块
├── styles/         # Tailwind 入口 + 移植自 Pi Web 的设计令牌
├── types/          # 协议类型，必须与桥的 Go 结构体一致
└── lib/            # 加载边界，让 Vite 把重库拆成独立 chunk
```

## 设计要点

**1. 整轮渲染历史。** `history.html` 一个片段只含完整回合，往回翻页时
插入位置永远落在轮边界。Pi Web 按单条消息切片，会把已在屏幕上的 assistant
重新折进折叠组，视口内容被顶走——这是结构性问题，不是滚动公式能补的。

**2. 通用扩展通道。** 前端没有任何插件专属代码。全部走三张模板：
`setStatus`/`setWidget`/`notify`/`setTitle`/`set_editor_text` 只渲染，
`select`/`confirm`/`input`/`editor` 需回执。Pi 的 RPC 模式对外暴露的
能力是封闭集合，源码里明确不转的（`setFooter`/`custom`/…）前端不做。

**3. htmx 负责请求-响应，流式层只补增量。** 流式不用 htmx 每秒替换
HTML——逐 token 重渲染会丢光标、闪烁。用 `textContent` 追加，
`agent_settled` 后整轮重取权威结果。

**4. 重库全部惰性。** katex / mermaid / xterm 通过间接动态 import 隔离成
独立 chunk，只在出现对应节点时下载。首屏预算写死在 manifest 里，
`pnpm check` 超预算即失败，且会递归统计入口的全部静态依赖——
只算入口文件会漏掉被静态引用的子 chunk（实际发生过：漏算时 24 KB，
算全后 33 KB）。

**5. TypeScript 7 的严格检查当扫帚。** TS 7 新增 TS6192（整个 import 语句
都未使用），抓出过 `terminal.ts` 整套 60 行封装从未被调用、
`notifyFromPiEvent` 从未被调用——「看起来接了其实没接」比没有更糟。

## 测试

```bash
pnpm test   # vitest，31 项
```

覆盖四类容易出静默错误的地方：

- **协议与回执**（`workbench.test.ts`）：requestId 关联、超时不重发、
  断线不伪造结果、状态轮询不替换正在编辑的扩展对话
- **滚动锚点**（`scroll.test.ts`）：`isAtBottom` / `captureDistance` /
  `restoreDistance`，翻页后视口内容零位移
- **内容净化**（`content.test.ts`）：Markdown 经 DOMPurify，脚本与
  `javascript:` URL 一律剥掉
- **终端资源**（`terminal.test.ts`）：连续按键合并为一次请求、
  断线时暂停输入且不宣称服务端资源已释放

浏览器侧行为（滚动、扩展对话、终端关闭后的服务端残留）用 agent-browser
实测，不靠断言代替。

## 体积

预算写死在 `ui-manifest.json` 的 `build.firstLoadBudgetGzipKB`（当前 40 KB），
`pnpm check` 按**入口的静态依赖闭包**统计——只算入口文件会漏掉被静态引用的
子 chunk（实测漏算时 24 KB，算全后 34 KB）。

但**预算数字只是包大小，不是传输大小**。桥必须真的压缩，否则浏览器收到的是
118 KB 原文。桥侧已实现 `br` / `gzip` 双算法协商：

| | 不压缩 | gzip | brotli |
|---|---:|---:|---:|
| app.js | 96934 | 28856 | 25605 |
| app.css | 21118 | 5478 | 4820 |
| shell HTML | 9925 | 3959 | 3398 |
| 会话列表 | 1314 | 404 | 351 |
| 历史片段 | 15129 | 1850 | 1578 |
| **合计** | **141 KB** | **39.6 KB** | **34.9 KB** |

惰性 chunk 合计 2.4 MB（按需）。对照 Pi Web 首屏 gzip 0.91 MiB / 解码 2.93 MiB。

**已测量并否决的进一步缩减：**

- 换用 htmx 自带的 `htmx.min.js`（52 KB 预压缩）而非 Vite 树摇后的 ESM：
  只省 738 B gzip，却要放弃模块图并改成 script 注入。
- 去掉 5 套主题中的 3 套：5 套加起来才约 1.2 KB gzip。
- 把 htmx 拆成后置 chunk：不减少总字节（UI 功能依赖它），只改变顺序，
  对总传输量没有收益。

首屏 34.9 KB 里 htmx 约占 15.5 KB（44%），是唯一还可能大幅缩减的部分，
但替换它等于手写一套声明式交换层，代码量会超过 htmx 本身。

## 已验证的浏览器行为

隔离环境（真实 Go 桥 + `tests/fixtures/pi-rpc.mjs` 假 Pi，不调用付费模型）：

- 浏览历史不启动 Pi；翻页零重复，阅读锚点位移 0 px
- 发送 → 流式增量 → `agent_settled` 后整轮重取，代码块全部高亮
- 扩展 `confirm` 对话：WS 推来 → 对话框渲染 → 回执 → 状态回就绪
- `setStatus` / `setWidget` / `notify` 三类只读通道均正确呈现
- 文件浏览与预览（按扩展名给 hljs 语言提示，Markdown 不再被猜成 Python）
- 终端打开、输入、关闭后服务端注册表归零
- 桥重启后自动重连并重新同步，不自动重发命令
- 390px 视口无横向溢出；axe WCAG 2A/2AA 违例为 0
