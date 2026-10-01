# AGENTS.md

## 项目背景

本仓库是**自托管的 Pi 工作台**：一个 Go 桥 + 一个 HTMX 前端，浏览器通过桥驱动
本机的 `pi --mode rpc` 子进程。仓库由两个原独立仓合并而来（历史完整保留），
两个目录并排：

| 目录 | 内容 |
|---|---|
| `pi-bridge-go/` | Go 桥：HTTP/WS 接入、worker 生命周期、会话索引、工作区、终端、relay/隧道 |
| `pi-webui-htmx/` | HTMX 前端与 UI 包：Go 模板、TypeScript 交互增强、三表 CSS |

改动可能同时落在两边（例如新增片段端点要改桥的渲染函数 + 模板 + hx 属性和契约测试）。

## 固定环境

- Go 1.27.1（`go.mod`）；Node ≥ 20.19，pnpm 11.22（`packageManager` 锁定）
- Pi：`pi --mode rpc`，开发基准 0.85.1（协议 v1）
- 本机生产：`pi-bridge.service` 绑 EasyTier 地址 `10.0.0.1:39082`，
  `--public-origin https://203.0.113.10:39080`，服务器侧由独立 caddy 网关反代；
  产物在 `pi-work/deploy/`（工作区外，非本仓）
- 改前端产物后**必须重启桥**（桥在启动时快照 manifest 与入口资源名，刷新页面无效）

## 硬约束

**数据权威**：Pi 的 JSONL 是会话正文、工具结果与分支的唯一权威。桥持久化只允许：
配对设备、允许根、桥设置、可重建的本地索引；SQLite 只用于桥元数据，**绝不作为
Pi 会话的第二份副本**。Magic Context 的 `context.db` 只读打开（`mode=ro`）。

**安全边界**：网页不能新增或修改 `!command` 形式的凭据表达式；模型密钥只留在桥
所在机器。桥不是沙箱——Pi 及其扩展以用户身份运行，工作区检查不是 OS 隔离。
反代注入上游凭据时，网关认证必须覆盖全部路径（被排除的路径会连带拿到注入凭据）。

**跨目录改动**：两侧分别提交并同时通过才算完成；`PI_WEBUI_DIR=../pi-webui-htmx
go test -race ./...` 覆盖跨目录契约，缺 UI 直接失败。

**中文规范**：注释、日志、错误消息、文档用中文；标识符与协议字段保持英文。
改任何中文文案前先查术语表 `pi-work/research-prompt/MC-zh-glossary.md`（唯一术语来源）。

**前端预算**：首屏自有代码 30 KiB / 总计 50 KiB（gzip），由 `pnpm check` 强制；
KaTeX、Mermaid、xterm 必须按需动态导入。

## 常用命令

```bash
# 桥
cd pi-bridge-go
gofmt -l . ; go vet ./... ; staticcheck ./...     # 必须全干净
go test -race -count=1 ./...                      # 19 个包
PI_WEBUI_DIR=../pi-webui-htmx go test -race ./... # 含跨目录契约

# 前端
cd pi-webui-htmx
pnpm test && pnpm typecheck && pnpm build && pnpm check
```

开发循环见 [pi-webui-htmx/docs/DEVELOPMENT.md](pi-webui-htmx/docs/DEVELOPMENT.md)
与 [pi-bridge-go/docs/DEVELOPMENT.md](pi-bridge-go/docs/DEVELOPMENT.md)。

## 工作方式

- 改动前先写**准确反例**（红），再改实现；完成后跑全量验证（上面两条命令）。
- 涉及订阅、游标、快照语义时，同时验证补发顺序（先补发、后确认）与迟到响应的归属守卫。
- 报告进度时以代码与实测为依据，不用「看起来没问题」结论；未验证项明确标注。

## 文档

| 文档 | 内容 |
|---|---|
| [README.md](README.md) | 项目总览与快速开始 |
| [pi-bridge-go/README.md](pi-bridge-go/README.md) | 桥：安装、选项、首次模型配置、部署形态 |
| [pi-bridge-go/api/v1/protocol.md](pi-bridge-go/api/v1/protocol.md) | 协议 v1：入口、方法、事件、限额 |
| [pi-bridge-go/docs/DEVELOPMENT.md](pi-bridge-go/docs/DEVELOPMENT.md) | 桥的开发细节与已知限制 |
| [pi-webui-htmx/docs/DEVELOPMENT.md](pi-webui-htmx/docs/DEVELOPMENT.md) | 前端开发细节与常见坑 |
