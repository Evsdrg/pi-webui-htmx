# pi-webui-htmx

Pi Bridge 的 htmx 前端。模板与静态资源归本仓，桥只提供数据。

**契约见 [`docs/contract.md`](docs/contract.md)。** 改模板样子不需要动桥；
改数据字段名、URL 或协议版本才需要。

## 工具链

pnpm + Vite 7 + TypeScript 5 + Tailwind v4。

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
独立 chunk，只在出现对应节点时下载。首屏预算 32 KB gzip 写死在 manifest 里，
`pnpm check` 超预算即失败。

## 体积

| | gzip |
|---|---:|
| 首屏（app.js + app.css） | **27 KB** |
| 惰性 chunk 合计 | 2.4 MB（按需） |

对照 Pi Web 首屏 gzip 0.91 MiB / 解码 2.93 MiB。
