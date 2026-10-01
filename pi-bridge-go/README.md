# Pi Bridge

自托管的 **Pi 工作台**：一个 Go 桥 + 一个 HTMX 前端。

桥通过子进程驱动 `pi --mode rpc`，把会话、工具调用、终端与工作区文件直接渲染成 HTML 片段推给浏览器。
**不嵌入 Pi SDK，不复制会话数据**——会话正文始终以 Pi 的 JSONL 文件为准。

```
浏览器  ⇄  桥（Go）  ⇄  pi --mode rpc 子进程
             │
             └─ 读取 Pi 的 JSONL / 只读 Magic Context 的 SQLite
```

## 能做什么

- **会话工作台**：会话列表、历史翻页、分支切换、消息流（思考块与工具调用各自折叠）、实时流式输出、中止
- **模型与设置**：图形化编辑 `models.json`（供应商 / 模型两级树、思考等级映射、连通性测试）、5 套主题、字号与内容宽度
- **工作区**：侧栏文件树、文件预览、只读 Git 变更视图、终端面板
- **扩展与记忆**：已装扩展清单、Magic Context 记忆面板（只读浏览、分类与项目筛选、完整正文）
- **多端接入**：公网可通过网关或云端中转（relay）接入，本机只需出站连接

## 前置要求

| 组件 | 要求 |
|---|---|
| [Pi](https://github.com/earendil-works/pi) | 已安装且可执行 `pi --mode rpc`（协议版本 1；开发基准 0.85.1） |
| Go | 1.27+（构建桥） |
| Node.js + pnpm | Node ≥ 20.19、pnpm 11（构建前端） |

## 快速开始

三个终端是三个步骤，也可以合并成脚本。

### 1. 构建前端

```bash
cd pi-webui-htmx
pnpm install --frozen-lockfile
pnpm build          # 产出 dist/，桥会从这里读资源
```

### 2. 启动桥

```bash
cd pi-bridge-go

export PI_BRIDGE_TOKEN="$(openssl rand -hex 32)"   # 登录用的访问令牌，至少 32 字符

go run ./cmd/pi-bridge \
  --workspace "$HOME/projects" \
  --pi "$(command -v pi)" \
  --ui-dir ../pi-webui-htmx \
  --state-dir "$HOME/.local/state/pi-bridge"
```

- `--workspace` 是**授权目录**：网页只能在这个根目录内浏览文件、开终端、起会话。
- `--ui-dir` 指向刚构建的前端目录。**不指定时桥只提供 JSON/WS API**，没有网页界面。
- `--state-dir` 保存桥自有状态；别放在 `/tmp`。

### 3. 打开浏览器

访问 <http://127.0.0.1:30142>，在登录框里粘贴上面那个 `PI_BRIDGE_TOKEN`。

令牌只用于换取当前浏览器的 Cookie，之后不再需要输入。

> 页面上点「＋ 新建会话」选目录，发第一条消息时才会真正拉起 Pi 进程。
> 只浏览历史不会启动任何进程。

## 首次使用：让桥看到你的模型

桥默认使用**隔离的**配置目录（`--state-dir/agent`），**不会**自动读取你真实的 `~/.pi/agent`。
所以第一次打开时模型列表是空的，发消息也会提示「当前会话没有可用模型」。二选一：

```bash
# 方案一：让桥直接用你现有的 Pi 配置（含 API 凭据）
--agent-dir "$HOME/.pi/agent"

# 方案二：保持隔离，只把模型配置拷进去
mkdir -p "$HOME/.local/state/pi-bridge/agent"
cp "$HOME/.pi/agent/models.json" "$HOME/.local/state/pi-bridge/agent/"
```

也可以在界面里点输入栏右侧的 `<>` 按钮直接编辑模型配置（供应商 / 模型 / 密钥 / 思考等级映射），
保存后写回 `--agent-dir` 下的 `models.json`。

**凭据只保存在桥所在的机器上**，不会发送到浏览器。

## 常用选项

| 选项 | 默认 | 说明 |
|---|---|---|
| `--listen` | `127.0.0.1:30142` | 监听地址。绑非环回地址时**必须**同时给 `--public-origin` |
| `--public-origin` | 空 | 对外访问来源，如 `https://pi.example.com`。声明后 Host/Origin 也接受它，Cookie 的 `Secure` 跟随其 scheme |
| `--workspace` | 必填 | 授权工作区根目录 |
| `--pi` | `pi` | Pi 可执行文件路径 |
| `--ui-dir` | 空 | 前端目录；为空则只提供 API |
| `--state-dir` | 缓存目录 | 桥自有运行目录 |
| `--agent-dir` | `state-dir/agent` | Pi 配置目录；默认用隔离目录，不会自动读取你真实的 `~/.pi/agent` |
| `--extensions` | 关 | 加载 Pi 已配置的扩展；即使开启，项目信任策略仍然保持拒绝 |
| `--idle-timeout` | `2m` | 空闲工作进程的回收时间 |
| `--max-workers` / `--max-terminals` | 4 / 4 | 并发上限 |
| `--relay` / `--device-id` / `--device-name` | — | 通过云端转发器接入（见下） |

完整列表：`go run ./cmd/pi-bridge --help`。

## 范围：刻意不做的事

明确不提供，请不要期待（也不会因请求而改变）：

| 不做 | 原因 |
|---|---|
| OAuth 设备码登录、供应商额度查询 | 本项目的用法是自备 API 凭据 |
| 插件/技能的远程安装、更新、搜索与任意 CLI 透传 | 远程执行面；包由本机 CLI 管理，网页只读清单与版本 |
| 任意会话正文写入、会话导入 | 会话持久化归 Pi，桥只读 |
| 伪装 TUI 独有能力（自定义 footer/header/editor 组件、原地树导航、reload） | 这些不在 RPC 命令表里，装上开关也做不到 |

暂缓或未立项：Web Push、PWA、版本自检与自动更新、PDF 导出、Minimap、多语言界面、worktree。
「未实现」不等于「刻意不要」，这些由产品范围另行决定。

## 部署形态

| 形态 | 说明 |
|---|---|
| **本机** | 桥绑环回，浏览器直连。最简单，无需证书。 |
| **公网（反向代理）** | 桥绑在私有/overlay 地址并声明 `--public-origin`；前面放一个终止 TLS 的网关。注意：网关的 Basic 认证**必须覆盖全部路径**，否则会连注入的上游凭据一起放行。 |
| **云端中转（relay）** | 桥主动出站连到 relay，无需公网入站端口；浏览器访问 relay 的设备前缀路径。relay 只转发帧，不解析载荷。 |

安全边界：桥自带 token/Cookie 鉴权、Host/Origin 严格核对、工作区沙箱（文件与终端都限制在 `--workspace` 内）、
模型凭据只保存在桥所在的机器上。**但桥不是沙箱**：Pi 及其扩展以你的用户身份运行，请只把 `--workspace` 指向可信目录。

## 文档

| 文档 | 内容 |
|---|---|
| [../README.md](../README.md) | 项目总览、技术栈、许可证 |
| [api/v1/protocol.md](api/v1/protocol.md) | 协议 v1：入口、方法、事件与限额 |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | 开发说明：设计边界、代码结构、不变量、限制 |
| [docs/method-inventory.md](docs/method-inventory.md) | 方法分类（由测试与代码交叉校验） |

## 许可证

**AGPL-3.0-or-later**（见仓库根的 [LICENSE](../LICENSE)）。

依赖组件各自遵循其原许可证；技术栈与 EPL-2.0 边界见仓库根的 [README](../README.md)。
