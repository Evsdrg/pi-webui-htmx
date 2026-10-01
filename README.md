# Pi Bridge

自托管的 **Pi 工作台**：一个 Go 桥 + 一个 HTMX 前端。

桥通过子进程驱动 `pi --mode rpc`，把会话、工具调用、终端与工作区文件直接渲染成
HTML 片段推给浏览器。**不嵌入 Pi SDK，不复制会话数据**——会话正文始终以 Pi 的
JSONL 文件为准。

```
浏览器  ⇄  桥（Go）  ⇄  pi --mode rpc 子进程
             │
             └─ 读取 Pi 的 JSONL / 只读 Magic Context 的 SQLite
```

## 能做什么

- **会话工作台**：会话列表、历史分页、分支切换、消息流（思考块与工具调用各自折叠）、实时流式输出、中止
- **模型与设置**：图形化编辑 `models.json`（供应商/模型两级树、思考等级映射、连通性测试）、5 套主题、字号与内容宽度
- **工作区**：侧栏文件树、文件预览、只读 Git 变更视图、终端面板
- **扩展与记忆**：已装扩展清单、Magic Context 记忆面板（只读浏览、分类与项目筛选、完整正文）
- **多端接入**：本机直连、反向代理（网关终止 TLS）、云端中转（relay）三种形态，同一份产物自适应

## 快速开始

需要已安装 [Pi](https://github.com/earendil-works/pi)（可执行 `pi --mode rpc`）、
Go 1.27+、Node.js ≥ 20.19 与 pnpm 11。

```bash
# 1. 构建前端
cd pi-webui-htmx
pnpm install --frozen-lockfile
pnpm build                      # 产出 dist/，桥会从这里读资源

# 2. 启动桥
cd ../pi-bridge-go
export PI_BRIDGE_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/pi-bridge \
  --workspace "$HOME/projects" \
  --pi "$(command -v pi)" \
  --ui-dir ../pi-webui-htmx \
  --state-dir "$HOME/.local/state/pi-bridge"

# 3. 打开 http://127.0.0.1:30142 并粘贴上面的令牌
```

首次使用时模型列表是空的——桥默认使用**隔离的**配置目录，不会读取你真实的
`~/.pi/agent`。用 `--agent-dir "$HOME/.pi/agent"` 直接沿用现有配置，或在界面里
点输入栏右侧的 `<>` 按钮编辑模型。

完整说明（选项、首次配置、部署形态、安全边界）见 **[桥的 README](pi-bridge-go/README.md)**。

## 设计原则

- **会话数据只有一份**：Pi 的 JSONL 是正文、工具结果与分支的唯一权威；桥只读、不复制。
  改这些数据的只有 Pi 自己。
- **浏览不启动进程**：列表、历史、文件浏览都只读磁盘；只有发消息或显式恢复会话才拉起 `pi` 子进程。
- **断线不等于取消**：命令被接受不等于完成；结果未知时不自动重发；浏览器断开不影响已受理的任务。
- **工作区是授权目录，不是沙箱**：`--workspace` 限定网页能碰的范围，但 Pi 工具与终端以**你的
  用户身份**运行——请只指向可信目录。
- **凭据只留在桥所在的机器上**：模型 API Key 不发送到浏览器；网页也不能新增命令型凭据表达式。
- **不可信内容不升级信任**：模型输出、文件内容与会话标题一律按不可信处理，经转义与净化后渲染。

## 仓库结构

| 目录 | 内容 |
|---|---|
| [`pi-bridge-go/`](pi-bridge-go/) | Go 桥：HTTP/WS 接入、worker 生命周期、会话索引、工作区、终端、relay/隧道 |
| [`pi-webui-htmx/`](pi-webui-htmx/) | HTMX 前端与 UI 包：Go 模板、TypeScript 交互增强、三表分职的 CSS |

## 文档

| 文档 | 内容 |
|---|---|
| [桥 README](pi-bridge-go/README.md) | 安装、启动、选项、首次模型配置、部署形态、刻意不做的事 |
| [协议 v1](pi-bridge-go/api/v1/protocol.md) | 入口、方法、事件与限额 |
| [桥开发说明](pi-bridge-go/docs/DEVELOPMENT.md) | 桥：设计边界、代码结构、不变量、测试、限制 |
| [前端开发说明](pi-webui-htmx/docs/DEVELOPMENT.md) | 前端：目录结构、技术栈、职责边界、样式、常见坑 |

## 测试

```bash
# 桥
cd pi-bridge-go
go vet ./... && go test -race ./...        # 19 个包
PI_WEBUI_DIR=../pi-webui-htmx go test -race ./...   # 含跨目录契约

# 前端
cd pi-webui-htmx
pnpm test && pnpm typecheck && pnpm build && pnpm check
```

## 技术栈

| | |
|---|---|
| 桥 | Go 1.27，标准库优先；外部依赖只有 **3 个**（brotli 压缩、coder/websocket、creack/pty），无 CGO，可纯静态构建 |
| 前端 | HTMX + Go 模板服务端渲染；TypeScript 仅做交互增强——**没有前端框架、路由、状态库** |
| 构建 | Vite 8 + TypeScript 7；Tailwind 4 只用于 preflight 复位（模板里没有工具类，样式是手写 CSS） |
| 浏览器侧依赖 | htmx、marked、highlight.js、KaTeX、DOMPurify、mermaid、xterm、ansi_up；除 htmx 外全部按需动态加载 |
| 会话数据 | Pi 的 JSONL 是唯一权威（含工具结果与分支），桥只读、不复制；桥自身只存设备配对、允许根与设置 |
| 要求 | Pi 可执行 `pi --mode rpc`（开发基准 0.85.1）；Go 1.27+；Node ≥ 20.19 与 pnpm 11 |

## 许可证

**AGPL-3.0-or-later**（见 [LICENSE](LICENSE)）。依赖组件各自遵循其原许可证，全部与 AGPL-3.0 兼容。

⚠️ **一处需要知道的边界**：前端构建产物含一个 **EPL-2.0** 的传递依赖（`mermaid` → `elkjs`），
而 EPL-2.0 与 GPL/AGPL 系列不兼容。这里要区分两种行为：

| 行为 | 影响 |
|---|---|
| **分发源码**（本仓库现状：`dist/` 与 `node_modules/` 都不入库） | 仓库里不含 EPL 代码，只有依赖声明 —— 无影响 |
| **分发构建产物**（发布 `dist/`、Docker 镜像、把 UI 内嵌进二进制） | EPL 代码随你分发，需先处理：移除 mermaid、改为可选运行时依赖，或从发布物中排除该 chunk |

另：AGPL 第 13 节要求通过网络对该软件提供服务的一方，向使用者提供对应版本的完整源码。
