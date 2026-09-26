# Pi Bridge 架构

状态：目标架构。A–E 阶段已实现，命令清单见 `api/v1/protocol.md`，
进度与验收见 `docs/pi-compatibility.md`。

## 1. 目标与边界

桥用 Go 实现，连接浏览器与独立 `pi --mode rpc` 进程，管理工作区、会话文件与终端。Pi 负责 agent、模型请求、工具、扩展、模型上下文及其会话持久化。云端不运行 Pi。

- 查看会话列表、历史、文件不创建 Pi worker。
- 本地桥不导入 Pi SDK，不构建模型上下文，不缓存无限长会话正文。
- 必要运行状态可以留在桥中：进程表、请求关联、订阅、有限重放缓冲、正在等待的扩展交互。
- 浏览器关闭或网络断开不等于取消任务。
- Pi JSONL 是会话正文权威来源；Magic Context 使用扩展自己的数据库。
- 内存收益必须在同会话、同插件和同工作负载下测量。RSS/匿名映射不能证明某个 JS 对象或框架独占了内存。

## 2. 部署角色

```text
浏览器：htmx + 局部 JavaScript
   │ HTTPS：静态资源、HTML 片段、文件
   │ WSS：命令、实时事件、扩展交互
   ▼
可选云端 pi-relay
   ▲ 本地桥主动建立带认证的 WSS 隧道
   │
本地 pi-bridge
   ├── Pi worker A ── session A
   ├── Pi worker B ── session B
   ├── 会话读取/索引
   ├── 文件/Git/工作区
   └── PTY
```

本地直连与云端转发复用应用协议。HTTP 历史请求可在 relay 转换成隧道请求；浏览器不必为了 htmx 改用自定义 WS DOM 协议。实时 JSON 事件由局部 JS 消费。

relay 只持有认证、设备路由和有界传输状态，不落盘会话正文，不加载 Pi 配置或模型密钥。TLS 中继会接触明文；无落盘不等于端到端加密。

## 3. 模块与依赖

```text
cmd/pi-bridge          本地入口
cmd/pi-relay           云端入口（后续阶段）
api/v1                协议规范、schema、示例
internal/app          配置、装配、关闭顺序
internal/protocol     封装、错误码、版本、能力
internal/transport    HTTP/WS、连接认证与限额
internal/relay        设备隧道路由
internal/auth         配对与授权
internal/runtime      worker 监督、生命周期、限额
internal/pi           Pi RPC 编解码、命令关联、版本适配
internal/sessions     JSONL、分支、目录与索引
internal/events       订阅、序号、重放、慢客户端处理
internal/workspace    文件、上传下载、Git、信任
internal/terminal     PTY
internal/management   模型、凭据、插件与技能管理
internal/presentation HTML 模板与页面数据
internal/storage      桥配置、索引与临时存储
internal/observe      日志、指标、诊断
```

目录随实际实现建立。A 阶段可把连接管理、有限事件分发集中在少数文件，出现独立职责后再抽包；禁止为了填满目录创建空抽象。

依赖方向：HTTP/WS -> 应用操作 -> runtime/sessions/workspace -> Pi RPC/文件/进程。handler 不直接启动命令；Pi 客户端不知道浏览器；历史读取不依赖 runtime。

### runtime 与 pi

`runtime` 管何时启动、暂停接收新工作、回收和限制 worker。`pi` 管 LF JSONL、命令 id、回复和事件。向 stdin 写入串行化，但发送下一命令不等待上一命令完成；否则长 prompt/bash 会挡住 abort。stdout 必须持续消费，stderr 单独排空，日志默认不记录正文和密钥。

Pi 启动无标准 ready 事件：以带 id 的 `get_state` 响应完成启动握手，并设超时。RPC 仅以 LF 分帧，允许末尾 CR；Unicode U+2028/U+2029 不是分隔符。限制帧字节数，不能使用默认 64 KiB 行上限，也不能无界读大帧。

## 4. 生命周期

```text
stopped -> starting -> idle -> running -> idle -> stopping -> stopped
                           -> waiting_input / retrying / compacting
                           -> failed
```

状态由请求、事件和必要的 get_state 对账更新，不靠浏览器是否还连接判断。

- session.start/new 是显式启动；history/list 不启动。
- 同一桥管理范围内，一个持久 session 最多一个 writer worker。外部 CLI 不在此互斥范围内；A 阶段使用独立会话目录，不能宣称已解决跨产品文件锁。
- `agent_end` 不是可靠的最终结束信号，`agent_settled` 才表示自动重试/压缩重试/排队续跑全部结束。直接 bash、扩展命令、交互请求还需各自跟踪。
- 待启动、在途命令、等待输入、重试、压缩、排队状态不做普通空闲回收。
- 没有任务且超过 idle timeout 才可回收；订阅本身不延长 worker 寿命。
- 进程退出是内存回收边界。停止需关闭 stdin、发进程组信号、限时等待，超时再强杀；确保子孙进程与管道均收敛。平台行为明确分开。
- 网络断线继续运行。桥进程退出时收敛受管 worker；桥崩溃后不承诺在途任务不中断，恢复持久会话。独立 supervisor/Unix socket 恢复是将来选项。
- 限制 worker 总数、连接数、在途命令数、单帧大小、订阅队列、重放容量和历史响应大小。

## 5. 数据所有权

| 数据 | 权威持久位置 | 桥内处理 |
|---|---|---|
| 会话正文/分支/工具结果 | Pi JSONL | 分页读取后释放 |
| 模型/凭据 | Pi 配置与凭据存储 | 受权管理，不返还密钥 |
| MC 记忆 | MC 数据库 | 扩展在 Pi 内使用 |
| 配对/允许根/桥设置 | 桥存储 | 小规模状态 |
| 会话路径/条目偏移 | 可重建索引 | 可以缓存，但有配额 |
| 实时重放 | 有界内存/短期 spool | 到期或超额清理 |

不另建一份会话正文数据库。后续 SQLite 只保存桥元数据、索引、必要命令回执。A 阶段允许请求内扫描 JSONL，但限制文件/条目/响应大小并释放；不能把 O(文件大小) 扫描描述成已经实现了磁盘索引。

### 历史正确性

1. 仅解析完整 LF 记录，忽略正在写入的末尾半行；完整但损坏的行返回诊断，不能静默吞掉。
2. 初版明确支持 v3，未知版本返回 unsupported；不擅自重写或迁移会话。
3. 默认叶子是磁盘可恢复叶子；运行中导航但未写入的叶子可能只在 Pi 内存中，接口必须标明来源，不能伪称读取了实时分支。
4. 显式 leafId 沿 parentId 取祖先；before 排除边界条目；返回稳定 ID。不存在、断链、循环和重复 ID 要检出。
5. UI 历史保留压缩前消息。不要把模型上下文重建逻辑拿来裁剪阅读历史。
6. 分页不重组已显示的 DOM 分组。长回合采用稳定分组 ID、分段与占位，不能为了整轮加载取消体积上限。
7. UI 记住 entryId + 视口偏移；图片或富文本后续改变高度时仍需锚点修正。

## 6. 协议与恢复

详见 `../api/v1/protocol.md`。协议归桥所有，Pi 事件通过明确命名的适配字段传递。

- requestId 是命令关联/去重标识，seq 是传输游标，entryId 是持久历史游标，不可混用。
- prompt accepted 不等于 completed。超时或断线不代表 Pi 没收到请求。
- 已写 stdin 的可变更命令不可盲目重试；结果不明返回 outcome_unknown。
- epoch 改变或重放窗口不足时明确要求重新同步，不能伪造无损恢复。
- 缺少活跃消息快照时，标记流中缺口，等权威 message_end 或持久记录。
- 大附件/下载与交互控制分别限额，避免一个大结果拖住 abort。

## 7. UI 仓库与安全边界

`pi-webui-htmx` 维护静态资源、局部 JS、CSS 和 HTML 模板；本地桥安装同版本模板包并提供页面数据。模板使用可信发布物，不能由网络请求任意上传后执行。模型输出和文件内容始终视为不可信数据，不能强制转为 template.HTML。

云端身份授权到 device，本地授权到 workspace/operation。连接认证、Origin/Host 检查、路径允许根不是同一个机制，分别验证。文件访问防路径遍历和越界 symlink；路径限制不是对 Pi 工具的沙箱。A 阶段默认仅 loopback，独立会话目录和显式工作区，尚不提供公网 relay。

凭据、配置修改、扩展安装都视为有副作用的操作；不会因为连接有认证就允许任意 RPC/命令透传。

## 8. 阶段与验收

| 阶段 | 功能 | 完成条件 |
|---|---|---|
| A | 本地协议、worker、历史、发消息/流式/取消 | 假 worker 的完整闭环 + 真 Pi 离线握手；读历史 0 worker；取消不排在长命令之后；退出回收 |
| B | 完整恢复、持久去重、容量、指标 | 断线/重试/慢客户端/崩溃边界可验证 |
| C | 分支、附件、文件、Git、PTY、模型配置 | 对照 Pi Web 能力清单逐项通过 |
| D | relay、主动隧道、配对、云部署 | 本地无公网入站也可用；权限边界测试 |
| E | 扩展补齐、OAuth、插件技能、搜索/导出/通知 | 兼容矩阵逐项有实现和证据 |

A 阶段测试不能自动调用付费模型。真实 Pi 只做隔离配置下的 get_state/握手/退出；流式 prompt 用可控制的假 worker 验证，报告必须明确区分两者。

## 9. 源码依据

基线：本机 `@earendil-works/pi-coding-agent` 0.85.1。

- 包内 README.md：RPC 模式、CLI 参数、project trust、offline、目录覆盖。
- docs/rpc.md：命令、接受语义、agent_settled、扩展 UI、LF framing。
- docs/session-format.md：v3 JSONL、树、压缩记录与持久历史。
- dist/modes/rpc/rpc-types.d.ts / rpc-mode.js：实现校验入口。
- dist/core/session-manager.js：文件载入、叶子恢复与持久化行为。

版本改变时先核对命令和序列化格式，再提升兼容声明。
