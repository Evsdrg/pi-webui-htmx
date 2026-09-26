# UI 包契约

本文件是 `pi-webui-htmx` 与 `pi-bridge-go` 之间唯一的约定。
两边各自演进，但只要本契约不被破坏就不需要同步改动。

**核心原则：模板归 UI 仓，桥只提供数据。** 桥不复制一份模板，
启动时从 `--ui-dir` 加载；目录缺失或校验失败时拒绝启动，而不是带着
不确定的模板跑。

---

## 1. 目录结构

```
pi-webui-htmx/
├── ui-manifest.json        # 契约本体，桥启动时读取
├── package.json            # 仅用于客户端依赖管理，不参与桥的构建
├── src/
│   ├── templates/          # 服务器渲染片段（htmx 直接换入 DOM）
│   │   ├── shell.html      # 应用外壳，唯一完整 HTML 文档
│   │   ├── sessions.html   # 侧栏会话列表
│   │   ├── history.html    # 一页历史，整轮渲染
│   │   ├── models.html     # 模型下拉框
│   │   ├── packages.html   # 已安装资源清单
│   │   ├── files.html      # 文件浏览
│   │   ├── diff.html       # unified diff 的服务端渲染
│   │   ├── extensions/     # 通用扩展通道三件套
│   │   │   ├── status.html # setStatus 状态行
│   │   │   ├── widgets.html# setWidget 小组件
│   │   │   └── dialog.html # select/confirm/input/editor 对话框
│   │   └── partials/       # 可复用局部（如 process.html）
│   ├── assets/
│   │   ├── app.css
│   │   ├── app.js          # 流式层，刻意保持小巧
│   │   ├── vendor/         # 第三方库原样，版本锁定并在 manifest 登记
│   │   └── lib/            # 我们写的薄封装：惰性加载 vendor，只在需要时注入
│   └── client/             # 需要模块化的客户端逻辑
├── dist/                   # 构建产物（带版本号，供云端静态分发）
└── docs/
    ├── contract.md         # 本文件
    └── components.md       # 组件选型与理由
```

### 谁拥有什么

| 资产 | 归属 | 桥能否修改 |
|---|---|---|
| `src/templates/**` | UI 仓 | ❌ 只读加载 |
| `src/assets/**` | UI 仓 | ❌ 只读服务 |
| `ui-manifest.json` | UI 仓 | ❌ 只读解析 |
| 会话数据、模型配置、进程 | 桥 | — |
| 流式协议帧 | 桥定义，UI 消费 | — |

---

## 2. 模板契约

### 2.1 通用规则

1. **除 `shell.html` 外，所有模板必须渲染片段，不是完整文档。**
   不输出 `<!DOCTYPE html>`、`<html>`、`<head>`、`<body>`。
2. **模板必须用 `html/template` 语法**（Go 侧渲染）。
   所有插值 `{{.X}}` 都会按 HTML 上下文自动转义。
   **禁止**把任何用户内容标成 `template.HTML`——模型输出、文件内容、
   会话标题一律按不可信数据处理。
3. **模板不得内联 `<script>`**。客户端行为放 `src/client/`，
   通过 `hx-on` 或事件监听接入。这条保证 CSP 可以收紧。
4. **模板不得内联 `style="..."` 承载动态值**。放 class，样式在 `app.css`。
5. **每个可交互元素必须带 `hx-*` 属性**，不能依赖客户端 JS 补发请求。
   流式对话是唯一例外（见 §4）。

### 2.2 数据契约

桥按下列结构渲染模板。字段名是契约的一部分，重命名属于破坏性变更。

#### `shell.html`

```go
map[string]string{"SessionID": string}
```

| 字段 | 含义 |
|---|---|
| `SessionID` | 当前会话；空串表示尚未选择 |

外壳必须把 `SessionID` 写进 `body[data-session-id]`，流式层靠它订阅。

#### `sessions.html`

```go
SessionsData{
    Items      []SessionRow
    Selected   string
    HasMore    bool
    NextOffset int
}
SessionRow{ID, Title, Modified string}
```

| 字段 | 约束 |
|---|---|
| `Items[].ID` | 必须是合法会话 ID，用于拼 `/ui/sessions/{id}/history` |
| `Items[].Title` | 已由桥做过长度截断，模板直接输出 |
| `HasMore` | 为真时必须渲染一个 `revealed` 触发的加载哨兵 |
| `NextOffset` | 哨兵的 `hx-get` 查询参数 |

#### `history.html`

```go
HistoryData{
    SessionID      string
    LeafID         string
    Turns          []Turn
    HasMore        bool
    OldestEntryID  string
}
Turn{ID, UserText, AssistantText string; Steps []Step; HasProcess bool}
Step{Kind, Detail string}
```

**整轮渲染是硬约束。** 一个片段只含完整回合，`hx-swap="afterbegin"` 或
`afterend` 插入时位置永远落在轮边界。理由：Pi Web 按单条消息切片，
往回翻页时会把已在屏幕上的 assistant 重新折进 `ProcessDetailsGroup`，
视口内容被顶走。整轮渲染从结构上排除这个问题。

`HasProcess` 为真时才渲染 `<details>`，避免空折叠组占位。

#### `models.html`

```go
ModelsData{Models []ModelRow; Current string}
ModelRow{ID, Name, Provider string}
```

`Current` 形如 `provider/id`，用于 `selected` 属性。

#### `packages.html`

```go
PackagesData{Packages []PackageRow}
PackageRow{Name, Source, Version, Latest string; HasUpdate, Disabled bool; Error string}
```

**只读清单。** 模板不得渲染安装、卸载、更新按钮——桥没有实现这些命令。

#### `files.html`

```go
FilesData{Root string; Entries []FileRow; Truncated bool}
FileRow{Name, Path string; IsDir bool; Size string}
```

`Path` 已是桥校验过的绝对路径，模板直接用于 `hx-get`。

#### `diff.html`

```go
DiffData{SessionID string; Files []DiffFile}
DiffFile{Path string; IsNew, IsDeleted, IsBinary bool; Lines []DiffLine}
DiffLine{Kind string; OldNo, NewNo int; Text string}
```

`Kind` 取 `add`/`del`/`context`/`hunk`，模板据此加 class。

**diff 由服务端渲染，不引 diff2html。** 理由：diff2html 3.4.56 起只发
CJS/ESM，没有浏览器包，而本仓刻意不引入构建步骤；同时服务端渲染
更符合「htmx 换入片段」的模型——桥已经在跑 `git diff`，多走一步解析
比在浏览器里再加载一个库更省。

### 2.2.1 manifest 的路径基准

`ui-manifest.json` 里所有相对路径均相对于 `src/`。
以 `_` 开头的键是说明，不是模板或资源，校验时跳过。

### 2.3 htmx 属性约定

| 场景 | 属性 |
|---|---|
| 侧栏初始加载 | `hx-get="/ui/sessions" hx-trigger="load, every 10s" hx-swap="innerHTML"` |
| 往上翻历史 | `hx-trigger="revealed" hx-swap="afterend"` |
| 切换会话 | `hx-get="/ui/sessions/{id}/history" hx-target="#turns" hx-swap="innerHTML"` |
| 发送消息 | `hx-post="/ui/sessions/{id}/prompt" hx-swap="none"` |
| 模型切换 | `hx-post="/ui/sessions/{id}/model" hx-swap="none"` |

**滚动位置由桥的响应头控制，不由模板里的 JS 控制。**
桥在历史响应返回 `X-Scroll-Mode: prepend` 或 `append`，客户端据此决定
保持离底部距离还是滚到新片段。模板不写滚动逻辑。

---

## 3. 通用扩展通道

前端**不得**为任何具体插件写专属代码。全部走这三张模板。

### 3.1 Pi 对外暴露的能力是封闭集合

源码（`dist/modes/rpc/rpc-mode.js` 的 `createExtensionUIContext`）确认：

**需要回执**（阻塞等结果，前端必须回复）：

| method | 请求字段 | 响应字段 |
|---|---|---|
| `select` | `title`、`options[]`、`timeout` | `value` 或 `cancelled` |
| `confirm` | `title`、`message`、`timeout` | `confirmed` 或 `cancelled` |
| `input` | `title`、`placeholder`、`timeout` | `value` 或 `cancelled` |
| `editor` | `title`、`prefill` | `value` 或 `cancelled` |

**无需回执**（fire-and-forget，前端只渲染）：

| method | 字段 |
|---|---|
| `setStatus` | `statusKey`、`statusText` |
| `setWidget` | `widgetKey`、`widgetLines[]`、`widgetPlacement` |
| `notify` | `message`、`notifyType` |
| `setTitle` | `title` |
| `set_editor_text` | `text` |

**Pi 明确不往 RPC 转的**（前端不做，也不假装有）：

`setFooter`、`setHeader`、`setWorkingMessage`、`setWorkingVisible`、
`setWorkingIndicator`、`setHiddenThinkingLabel`、`custom`、
`onTerminalInput`、`addAutocompleteProvider`、`setEditorComponent`、
`getEditorComponent`、`pasteToEditor`

### 3.2 状态行合并规则

多个插件的 `setStatus` 按 `statusKey` 去重，同 key 后者覆盖前者。
渲染顺序按 key 排序，保证同一条状态线在多次渲染间稳定，不跳动。

### 3.3 对话框回执

对话框模板必须带 `data-dialog-id`，提交时调桥命令：

```json
{"version":1,"kind":"command","requestId":"...","sessionId":"...",
 "method":"session.ui_response",
 "params":{"id":"<dialog-id>","value":"...","confirmed":true,"cancelled":false}}
```

三者语义互斥：`cancelled` 优先，其次 `confirmed`（confirm 用），
其余用 `value`（select/input/editor 用）。

---

## 4. 流式层：htmx 负责不到的 10%

流式对话**不**用 htmx 每秒替换 HTML。Pi Web 的教训是逐 token 重渲染
整条消息会丢光标、闪烁。

分工：

| 层 | 负责 |
|---|---|
| htmx + 服务器片段 | 侧栏、历史、设置、文件、模型、包清单、扩展状态与对话框 |
| WS + `src/assets/app.js` | 文本增量、运行状态、终端 IO |

`app.js` 只做四件事：

1. 连 WS，断线指数退避重连
2. `session.subscribe`，把 `body[data-session-id]` 带上
3. 收到 `message_update` 的 `text_delta` 时 `textContent` 追加，**不**重解析已有节点
4. 收到 `agent_settled` 后触发一次 `#turns` 的 `load`，整轮重取

重取而不是增量拼接，是为了拿到权威的 `message_end` 结果——包括工具调用、
思考块和最终文本的完整结构。

---

## 5. 协议与版本协商

### 5.1 握手

UI 加载时调 `GET /api/v1/capabilities`，校验：

- `version == manifest.protocolVersion`
- `manifest.requiredMethods` 中每个方法都出现在 `methods` 里

任一不满足，外壳显示不兼容提示，**不**进入半可用状态。

### 5.2 版本字段

| 字段 | 位置 | 变更规则 |
|---|---|---|
| `protocolVersion` | `ui-manifest.json` | 桥侧协议破坏性变更时 +1 |
| `uiVersion` | `ui-manifest.json` | UI 自身演进，桥不校验 |
| `piBaseline` | `ui-manifest.json` | 声明测试过的 Pi 版本，仅提示 |

### 5.3 兼容矩阵

UI 不得调用 `requiredMethods` 之外的命令做核心流程。
可以调，但调用前必须先在 `capabilities.methods` 里确认存在，
否则显示「当前桥不支持」。

---

## 6. 加载方式

桥按以下优先级找 UI 包：

1. `--ui-dir` 指定的目录（开发、云端自托管）
2. 编译期内嵌的副本（`pi-bridge` 独立分发时）

启动时校验：

- `ui-manifest.json` 可解析
- `templates` 中每个路径都存在
- `assets` 中 `app.css`、`app.js`、`htmx` 存在（其余可缺，缺的走 CDN 或禁用该特性）
- `protocolVersion` 与桥一致

任一失败：**拒绝启动**并打印缺什么。不静默降级到半套 UI。

---

## 7. 变更规则

| 变更 | 是否需要改桥 |
|---|---|
| 改模板内部结构、样式、class 名 | ❌ |
| 增删模板（同时更新 manifest） | ❌（桥按 manifest 找） |
| 改 `src/client/`、`src/assets/lib/` | ❌ |
| 给已有模板**增加**数据字段 | ❌（桥多给，模板不用） |
| **重命名或删除**数据字段 | ✅ 契约破坏 |
| 改 `hx-get`/`hx-post` 的 URL | ✅ |
| 增加对桥命令的依赖 | ✅（要进 `requiredMethods`） |
| 改 `protocolVersion` | ✅ |

最后一条的含义：**UI 可以随时改样子，但不能单方面改协议。**
