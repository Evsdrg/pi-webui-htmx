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

## 推荐用法

- 发第一条消息时才拉起 Pi 进程；只浏览历史不启动任何进程。
- `--workspace` 是**授权目录**：网页只能在这个根内浏览文件、开终端、起会话。
  桥不是沙箱——Pi 及其扩展以你的用户身份运行，请只指向可信目录。
- 模型凭据只保存在桥所在的机器上，不发送到浏览器。

## 仓库结构

| 目录 | 内容 |
|---|---|
| [`pi-bridge-go/`](pi-bridge-go/) | Go 桥：HTTP/WS 接入、worker 生命周期、会话索引、工作区、终端、relay/隧道 |
| [`pi-webui-htmx/`](pi-webui-htmx/) | HTMX 前端与 UI 包：Go 模板、TypeScript 交互增强、三表分职的 CSS |

## 文档

| 文档 | 内容 |
|---|---|
| [桥 README](pi-bridge-go/README.md) | 安装、启动、选项、首次模型配置、部署形态 |
| [架构](pi-bridge-go/docs/architecture.md) | S01–S12：目标形态、边界约束与关键设计决策 |
| [通信约定](pi-bridge-go/docs/communication.md) | 分层、受理序列、订阅与恢复、背压限额 |
| [协议 v1](pi-bridge-go/api/v1/protocol.md) | 入口、方法、事件与限额 |
| [Pi 兼容矩阵](pi-bridge-go/docs/pi-compatibility.md) | 上游能力对照与刻意排除项 |
| [技术栈](pi-bridge-go/docs/tech-stack.md) | 依赖与版本清单 |
| [开发说明](pi-bridge-go/docs/development.md) | 接口、测试证据、资源限制、后续优化 |
| [UI 包契约](pi-webui-htmx/docs/contract.md) | 模板字段、构建与交换守卫 |
| [组件选型](pi-webui-htmx/docs/components.md) | 前端依赖、样式组织与资源释放 |
| [许可证核查](pi-bridge-go/docs/licensing.md) | 依赖兼容性与 AGPL 边界说明 |

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

## 许可证

**AGPL-3.0-or-later**（见 [LICENSE](LICENSE)）。

依赖组件各自遵循其原许可证，核查结论见 [docs/licensing.md](pi-bridge-go/docs/licensing.md)
——其中有一条需要注意：前端构建产物包含一个 EPL-2.0 的传递依赖（mermaid → elkjs），
因此**分发源码**与**分发构建产物**的合规要求不同，该文档说明了三种处理方案。
