# pi-webui-htmx

Pi Bridge 的 htmx 前端。模板与静态资源归本仓，桥只提供数据。

**契约见 [`docs/contract.md`](docs/contract.md)。** 改模板样子不需要动桥；
改数据字段名、URL 或协议版本才需要。

## 结构

```
src/
├── templates/      # 服务器渲染片段，htmx 直接换入 DOM
│   ├── extensions/ # 通用扩展通道三件套（不针对任何具体插件）
│   └── partials/   # 可复用局部
└── assets/
    ├── lib/        # 我们写的薄封装：惰性加载 vendor
    └── vendor/     # 第三方库原样，版本锁定
```

## 设计要点

**1. 整轮渲染历史。** `history.html` 一个片段只含完整回合，往回翻页时
插入位置永远落在轮边界。Pi Web 按单条消息切片，会把已在屏幕上的 assistant
重新折进折叠组，视口内容被顶走——这是结构性问题，不是滚动公式能补的。

**2. 通用扩展通道。** 前端没有任何插件专属代码。全部走三张模板：
`setStatus`/`setWidget`/`notify`/`setTitle`/`set_editor_text` 只渲染，
`select`/`confirm`/`input`/`editor` 需回执。Pi 的 RPC 模式对外暴露的
能力是封闭集合，源码里明确不转的（`setFooter`/`custom`/… ）前端不做。

**3. htmx 负责请求-响应，约 90 行 JS 负责流式。** 流式不用 htmx 每秒
替换 HTML——逐 token 重渲染会丢光标、闪烁。用 `textContent` 追加，
`agent_settled` 后整轮重取权威结果。

**4. 不引构建工具。** 没有模块图需要打包。第三方库放 `vendor/`，
封装放 `lib/`，都在 `ui-manifest.json` 登记。

## 校验

```bash
node scripts/check-contract.mjs
```

检查 manifest 与磁盘一致、片段不含完整文档结构、无内联脚本、
无动态 style 插值、对话框带回执接线、扩展通道两类方法无重叠。

## 体积

首屏 gzip 约 85 KiB（htmx + app.css + app.js + marked/DOMPurify + highlight）。
mermaid 2.5 MB、xterm、KaTeX 全部懒加载。
对照 Pi Web 首屏 gzip 0.91 MiB / 解码 2.93 MiB。
