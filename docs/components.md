# 组件选型与理由

选型标准只有一条：**这个库在 htmx 的「服务器渲染片段」模型下是否仍然必要。**
不必要的宁可手写。

---

## 已选

### htmx 2.0.4

核心。声明式地把服务器响应换进 DOM，不需要构建步骤，不需要虚拟 DOM。
版本锁定在 `src/assets/vendor/` 并在 `ui-manifest.json` 登记。

### marked + DOMPurify

模型输出是 Markdown。htmx 换入的片段里 Markdown 仍是纯文本，
必须客户端渲染。**DOMPurify 不是可选的**——模型输出与文件内容都不可信，
`marked` 不过滤 HTML，两者必须配套。

渲染时机：`htmx:afterSwap` 后扫 `.markdown` 节点，未渲染过的才处理。

### highlight.js

代码块高亮。按需调用 `hljs.highlightElement()`，不做全局扫描。
语言包按需加载，不打包全部 190 种。

### KaTeX + auto-render

公式。同 highlight，懒加载。

### mermaid

图表。**最重的一个**，必须动态 `import()`，只在出现 `.mermaid` 节点时加载。
首屏不包含它。

### xterm.js + FitAddon

终端。这是唯一「不得不复杂」的组件——PTY 是字节流，没有 HTML 表示。
数据面走 WS，不走 htmx。

### （不选）diff2html → 服务端渲染

最初打算用 diff2html，实际查看包结构后放弃：3.4.56 起只发 CJS/ESM，
没有浏览器可用包，而本仓刻意不引入构建步骤。

改为**桥服务端解析 unified diff 并渲染 `diff.html` 片段**。这反而更贴合
htmx 模型——桥本来就在跑 `git diff`，多一步解析比在浏览器里再加载一个库
更省，而且 diff 的配色可以复用同一套 CSS 变量。

### ansi_up

bash 输出转 HTML。小，无依赖。

---

## vendor 清单

| 文件 | 来源 | 用途 |
|---|---|---|
| `htmx.min.js` | unpkg htmx@2.0.4 | 核心 |
| `marked.min.js` | unpkg marked@12.0.2 | Markdown |
| `dompurify.min.js` | unpkg dompurify@3.1.6 | HTML 净化（必需） |
| `highlight.min.js` | unpkg highlight.js@11.10.0 | 代码高亮 |
| `katex.min.js` | unpkg katex@0.16.11 | 公式 |
| `mermaid.min.js` | unpkg mermaid@11.4.1 | 图表（2.5 MB，懒加载） |
| `xterm.js` / `xterm.css` | unpkg @xterm/xterm@5.5.0 | 终端 |
| `fitaddon.min.js` | unpkg @xterm/addon-fit@0.10.0 | 终端自适应 |
| `ansiup.min.js` | unpkg ansi_up@5.2.0 | ANSI 转 HTML |

`src/assets/lib/` 下是我们写的薄封装，负责惰性加载与「只渲染未处理节点」。
第三方代码一律放 `vendor/` 不做修改，便于核对与升级。

---

## 刻意不选

| 需求 | 做法 | 理由 |
|---|---|---|
| UI 组件库（shadcn/MUI/Radix…） | 手写 + CSS 变量 | 这些库假设 React/Vue 的渲染模型，在 htmx 下只会徒增体积 |
| 图标库 | 内联 SVG，用哪个写哪个 | 图标库动辄几百 KB，实际用不到二十个 |
| 状态管理库 | DOM 即状态 | htmx 的模型下引入 Redux/Zustand 是自相矛盾 |
| i18n 框架 | 两份模板或服务端选语言 | 三句话的需求不需要框架 |
| 虚拟滚动 | 历史分页 + 整轮渲染 | 已经够用；虚拟滚动与 htmx 的 DOM 替换冲突 |
| 前端测试框架 | 不用 | 契约由 Go 侧测试守住（模板渲染、转义、数据形状），客户端只做 DOM 粘合 |

### 关于 Vite / pnpm / TypeScript（0.2.0 起已采用）

0.1.0 时判断「不值得」：自己写的代码只有 340 行 / gzip 4 KB，
引工具链省不下体积。用户的要求是「工具链可以重，最终产物足够轻」，
目标从「省工具链」变成「保产物」，于是三项都上。

各自解决的真实问题：

| 工具 | 解决的问题 | 无它会怎样 |
|---|---|---|
| **pnpm** | 依赖图与锁文件 | `curl` 拉包、手工记版本，升级时无法确认兼容性 |
| **Vite 7** | 代码分割 + 内容哈希 + 压缩 | 手工拆 chunk 不可能做好；无哈希则不能长期缓存 |
| **TypeScript** | 协议类型 | `src/types/protocol.ts` 305 行，把桥的 v1 协议、错误码全集、Pi 事件载荷、扩展 UI 通道全部类型化。没有它，`msg.data.assistantMessageEvent.type` 拼错只能运行时发现 |
| **Tailwind v4** | 与 Pi Web 一致的类名写法 | 手写 CSS 亦可，但迁移 Pi Web 的模板时要逐个翻译类名 |

**代价与约束**（这是关键，不是免费午餐）：

- 构建从零步骤变成一步 `pnpm vite build`
- 桥的加载逻辑必须读 `dist/.vite/manifest.json` 解析哈希文件名
- 首屏预算写进 `ui-manifest.json`，`check-contract.mjs` 超预算即失败——
  否则 TypeScript 的运行时类型助手和 Vite 的默认 chunk 策略会把重库
  悄悄拖回首屏（本次就实测发生过：静态 import katex/xterm 让首屏
  从 24 KB 涨到 105 KB）
- `dist/` 进 `.gitignore`，CI 必须构建后才能部署

---

## 首屏体积预算

| 资源 | gzip 后 |
|---|---:|
| htmx | ~13 KiB |
| app.css | ~3 KiB |
| app.js | ~3 KiB |
| marked + DOMPurify | ~35 KiB |
| highlight（核心 + 常用语言） | ~30 KiB |
| **首屏合计** | **~85 KiB** |

按需加载（不计入首屏）：KaTeX ~25 KiB、mermaid ~300 KiB、xterm ~80 KiB、diff2html ~40 KiB。

对照 Pi Web 首屏 gzip 0.91 MiB / 解码 2.93 MiB——主要是它把
mermaid + KaTeX + syntax-highlighter + xterm 全打了进去。

我们靠代码分割做到：**首屏 27 KB gzip，其余 2.4 MB 全部惰性。**

---

## CSP

因为模板不内联 `<script>`、不内联动态 `style`，可以上较严的策略：

```
default-src 'self';
script-src 'self';
style-src 'self';
img-src 'self' data:;
connect-src 'self' ws: wss:;
font-src 'self';
```

`connect-src` 需要 `ws:`/`wss:` 给流式层。若 UI 由云端托管而桥在本地，
还要加上云端与本地桥的 origin。
