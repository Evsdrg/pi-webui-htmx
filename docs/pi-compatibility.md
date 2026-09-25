# Pi 兼容矩阵

基线：本机 @earendil-works/pi-coding-agent 0.85.1。状态把上游支持与桥实现分开；上游存在命令不代表桥已支持。A 阶段只实现协议列出的子集。

## 证据

已完整核对包内 README.md、docs/rpc.md、docs/session-format.md。实现验收还需对照包内 dist/modes/rpc/rpc-mode.js、rpc-types.d.ts、dist/core/session-manager.js，并使用隔离进程做无模型请求握手。

本机包目录：

`/opt/devTools/files/pnpm/global/v11/1944e6-18d6e892633ca2c8-0/node_modules/.pnpm/@earendil-works+pi-coding-agent@0.85.1_ws@8.21.3/node_modules/@earendil-works/pi-coding-agent`

这些绝对路径是核对依据，不能硬编码进程序。运行时通过 --pi 配置可执行文件。

## 命令与功能

| 功能 | Pi 0.85.1 | 桥计划 | 注意事项 |
|---|---|---|---|
| 启动 RPC | ✅ --mode rpc | A | 不存在标准 ready；get_state 做握手 |
| 创建/恢复 | ✅ CLI --session / --session-dir | A | 显式启动；列表/历史不启动 |
| 发消息 | ✅ prompt | A | success 表示接受/排队，不是结束 |
| 打断 | ✅ clear_queue + abort | A | abort 单独使用会继续尚存队列 |
| 实时输出 | ✅ message_start/update/end 等 | A | update 是增量，不带累计 message |
| 完全收敛 | ✅ agent_settled | A | agent_end 之后可能重试或继续 |
| 状态 | ✅ get_state | A | 不应透传模型对象中的私有 headers/配置 |
| 换模型/强度 | ✅ set_model/set_thinking_level | C | 能力列表来自当前 Pi，不能猜 level |
| steering/follow-up | ✅ steer/follow_up 或 prompt.streamingBehavior | A/C | A 支持 prompt 的明确排队选项；独立管理 C |
| 压缩/重试 | ✅ compact/set_auto_compaction/set_auto_retry/abort_retry | C | 单独 abort_compaction 不在标准命令表 |
| Bash | ✅ bash/abort_bash | C | 不是 PTY；输出超大时有 fullOutputPath |
| fork/clone | ✅ fork/clone | C | 会改变 worker session ID；runtime 必须先支持重绑定 |
| 切换会话 | ✅ switch_session/new_session | C | 可能被扩展取消；不能直接透传破坏进程表 |
| 树/原始条目 | ✅ get_tree/get_entries | C | append 游标不是分支分页游标 |
| 原始历史阅读 | ✅ v3 JSONL | A | 桥读盘，不调用 get_messages 拉整份内容 |
| 统计/导出/命名 | ✅ get_session_stats/export_html/set_session_name | C/E | 导出路径需授权；历史不得自己拼追加写入 |
| 命令目录 | ✅ get_commands | C | 不包含内置 TUI /settings、/hotkeys |
| 工具动态读取/设置 | ⚠️ 文档未列 get_tools/set_tools | C/E | 先核对源码；A 返回 unsupported，不伪造成功 |
| 分支原地导航 | ⚠️ 未列 navigate_tree/fork_branch | C/E | SDK/Pi Web 能力不能当作 RPC 支持 |
| reload | ⚠️ 未列 RPC reload | C/E | TUI /reload 不等于 RPC 命令 |
| select/confirm/input/editor | ✅ extension_ui_request/response | C | Pi ctx.hasUI=true，A 对 dialog 明确取消，不能永久挂起 |
| notify/status/widget/title/editor text | ✅ fire-and-forget UI 事件 | A/C | A 可转发，富界面 C |
| 自定义 TUI component | 🚫 custom() 返回 undefined | E 评估 | 需要网页替代，不做终端控件直译 |
| footer/header/editor component 等 | 🚫 RPC 下 no-op | E 评估 | capabilities 必须如实说明 |
| PTY/Git/文件/技能配置 UI | 不是 Pi RPC 标准职责 | C/E | 在桥独立实现 |

## 必须保留的语义

1. LF 是唯一 RPC 记录分隔符；U+2028/U+2029 不切行。CRLF 可接受。
2. 所有 request id 由桥生成内部唯一值，外部 requestId 不直接与扩展 dialog id 混用。
3. message_end.message 权威；流式渲染按 contentIndex 和事件构造。
4. tool_execution_update.partialResult 是累计工具输出，不能当字符串 delta 拼接。
5. get_entries(since) 包含所有分支、元数据、压缩前历史。
6. 初版不会启动 Pi 来读取磁盘历史，避免扩展初始化/格式迁移产生副作用。
7. 没有保存的 project trust 时 RPC 使用 Pi 的非交互默认；A 默认 --no-approve，明确不自动批准项目扩展。
8. --offline 只禁启动联网，不代表 prompt 不能调用模型。测试不得发送真实 prompt。
9. 默认不自动加载用户全局 MC/模型配置做冒烟。真实 Pi 测试使用临时 agent-dir，关闭扩展/技能/上下文发现。
10. 初版对未知协议/会话版本显式拒绝；新版测试通过前不扩展兼容声明。

## 验收清单

### 已通过（2026-09-26，本机）

- [x] `go vet ./...` 与 `go test -race ./...` 全部通过
- [x] 用可执行假 worker 验证启动、接受回复、异步事件和同时发送取消
- [x] Unicode 分隔符/CRLF/超大帧/半帧 EOF 的 JSONL framing 测试
- [x] 完整损坏历史记录、末尾半行、分支、重复 ID、断链、自环测试
- [x] `session.list`/`history` 不触发 worker 启动
- [x] 无授权、错误 Host、跨源 Origin、越界 cwd、符号链接逃逸被拒绝
- [x] 慢订阅者被摘除且不阻塞 stdout；写入背压下 `Notify` 不挂起
- [x] 浏览器断线不取消已启动任务；桥关闭/SIGKILL 后回收自己创建的进程
- [x] 真实 Pi 隔离配置握手与退出；无模型请求、无生产会话修改
- [x] 工作态实测：桥 10.2 MiB + Pi 工作进程 145 MiB

### 未完成

- [ ] 跨重启的持久命令回执与事件补发（B 阶段）
- [ ] 磁盘索引替代请求内扫描（B 阶段）
- [ ] 非 Linux 平台进程监督（当前显式报错）
- [ ] 与外部 `pi` CLI 的文件级互斥（未解决，仅限桥内单写者）
- [ ] 模型/思考强度、压缩、fork、bash、PTY、文件/Git（C 阶段）
- [ ] 云端 relay 与隧道（D 阶段）
