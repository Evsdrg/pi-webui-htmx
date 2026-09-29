# Go 惯用写法复核

复核日期：2026-09-30。基线：桥 `863041d` 与 `9592d4e`（工作树干净）。**本文是源码复核记录，不是修复声明；标出的问题都还没改。**

对象：`internal/` 下 18 个包与 `cmd/`，约 16,700 行非测试代码（另有 83 个测试文件、13,571 行）。`go.mod` 为 go 1.27.1，直接依赖 3 个（brotli、coder/websocket、creack/pty）。

本文分两轮：第一轮（G01–G19）看**语言惯用性**；第二轮（G20–G30）用基准与 pprof 看**内存分配、嵌套深度与重复**。

结论：代码在**并发安全与安全边界**上明显高于平均水平（无 `panic`/`goto`/`reflect`/`ioutil`，安全路径全部走 `crypto/rand` 与 `subtle.ConstantTimeCompare`，`context` 一律是首参与不入结构体，`http.Server` 有完整超时，测试没有在 goroutine 里调 `t.Fatal`）。不那么 Go 的地方集中在四块：**错误链几乎不存在**、**类型表达靠字符串与匿名容器**、**少数函数与构造器体量失控**、**几处把「能跑」当成「够快」的分配习惯**（第二轮量化）。

## 1. 复核基准

判断标准取官方文档，不取个人口味：

| 文档 | 用来判断 |
|---|---|
| [Effective Go](https://go.dev/doc/effective_go) | 命名、具名类型、小函数、方法集 |
| [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | 错误处理、报错文本、参数列表、接口位置、`context` |
| [Working with Errors in Go 1.13](https://go.dev/blog/go1.13-errors) | `%w`、`Unwrap`、`errors.Is/As` |
| [Go Doc Comments](https://go.dev/doc/comment) | 包注释与文档格式 |
| [time 包文档](https://pkg.go.dev/time#After) | Go 1.23 起 `time.After` 定时器可被 GC 回收 |

明确不作为问题依据的：项目规则 #227 要求注释、日志与对外提示用中文，因此**报错文本是中文不算违规**；协议字段名（`requestId`、`deviceId`）是 wire format，不是 Go 标识符；JSON 的 camelCase 由协议决定。

## 2. 复核方法

- 通读全部非测试源码；对重点文件（`transport`、`runtime`、`presentation`、`sessions`、`management`、`relay`）逐函数核对。
- 脚本化取证，避免凭印象：花括号配对测量函数体长度；剥离字符串与注释后配对 `go func` 字面量，检查 `t.Fatal` 是否落在 goroutine 内；逐包扫描包注释；统计 `%w`、`errors.Is/As`、`any`、`map[string]…` 返回、`encoding string` 参数的出现量。
- 计数都在 `863041d` 上采集，命令可复现。

没做的：本机未安装 staticcheck/golangci-lint，因此下文 G18 只给建议未给基线；未做性能基准以外的长时间内存观测；未逐条比对 B/U 台账（落修前应先查重，见第 8 节）。

## 3. 结论摘要

严重度按「对正确性/可维护性的实际影响」定，不按改动量。

| 编号 | 类别 | 严重度 | 位置 | 一句话 |
|---|---|---|---|---|
| G01 | 结构 | 高 | `internal/transport/server.go:1138` | 696 行、60 个 case 的分发函数，36 处匿名 map 返回 |
| G02 | 结构 | 中 | `internal/transport/server.go:102` | `New` 13 个位置参数，相邻两个 `string` 可互换而不报错 |
| G03 | 结构 | 中 | `server.go:245`、`:677` | `serveUI` 381 行、`ServeHTTP` 126 行；`encoding string` 穿过 11 个函数 |
| G04 | 错误 | 高 | `internal/protocol/protocol.go:30` 等 | 错误链断裂：`protocol.Error` 无 cause/`Unwrap`，全库仅 6 处 `%w` |
| G05 | 错误 | 中 | `claims.go:232`、`server.go:1113` | 回执写入失败被 `_ =` 静默丢弃，而回执是对账依据 |
| G06 | 错误 | 低 | `internal/storage/receipts.go:325` | 用 `os.ErrClosed` 表意「未启用」，借用标准库哨兵语义 |
| G07 | 类型 | 中 | `manager.go:251` 等 | 字符串当枚举（`status`、`encoding`）、裸布尔参数（`stop(force, idleOnly bool)`） |
| G08 | 类型 | 中 | `presentation.go:737`、`events/ring.go:78` | 匿名容器穿透层边界：`[]map[string]any`、`map[string]any`；全库 299 处 `any` |
| G09 | API | 中 | `internal/presentation` | 87 个导出符号、21 个 `Render*`；接口应定义在使用方（`transport`） |
| G10 | 并发 | 低 | `internal/transport/send_queue.go:33` | 配额等待用 2ms 轮询表达（非泄漏，Go 1.23 起定时器可回收） |
| G11 | 并发 | 低 | `manager.go:91`、`workspace/files.go:41` | 双锁纪律无注释，看不出哪个字段受哪把锁保护 |
| G12 | API | 中 | `presentation.go:64`、`server.go:258/720` | nil 接收者与显式 nil 检查两种约定并存 |
| G13 | 整洁 | 低 | `tunnel/client.go:270`、`terminal/proc_oth.go:13` | 两处 `var _ = …` 假引用 |
| G14 | 整洁 | 低 | `presentation.go:995` | `var Now = time.Now` 声称供测试替换，实际无人使用 |
| G15 | 整洁 | 低 | `events/ring.go:44` | `Push` 内一个不可达分支 |
| G16 | 文档 | 低 | `internal/transport` | 18 个包中唯一缺包注释 |
| G17 | 测试 | 低 | 全仓 | 417 个测试函数中 386 个名称含中文；`os.Setenv` 应换 `t.Setenv` |
| G18 | 工程 | 中 | `.github/workflows/check.yml` | 只有 gofmt/vet/race；G13–G15 都能在这套检查下存活 |
| G19 | 类型 | 低 | `internal/magiccontext` | sqlite3 命令行 + 代回 SQL；已在面板接入时说明，本轮只登记 |

第二轮新增（内存 / 嵌套 / 重复，原始数据见第 5 节）：

| 编号 | 类别 | 严重度 | 位置 | 一句话 |
|---|---|---|---|---|
| G20 | 内存 | 高 | `sessions/store.go:429` | 只为判断一个 kind，整条读盘并完整投影：首页基准里占 ~31KB/op |
| G21 | 内存 | 中 | `sessions/lazy.go:55`、`sessions/search.go:213` | 用 `json.Unmarshal` 的失败路径判断 content 是串还是数组；渲染基准里 ~9KB/op 花在构造错误对象 |
| G22 | 内存 | 中 | `sessions/store.go:343` | 每页 N 次 `make([]byte, node.size)`（96KB/op），可合并为一次；`selected`/`Entries` 未预分配 |
| G23 | 内存 | 低 | `jsonl/reader.go:24` | 每条记录一次 append 分配；扫描、搜索、惰性加载、Pi 读循环都走它 |
| G24 | 内存 | 中 | `transport/server.go:846` | `toAnyMaps` 为喂渲染层做 JSON 往返：500 项目录 590µs / 360KB / 7017 allocs |
| G25 | 内存 | 低 | `runtime/bash.go:90` | 按请求上限先 `make`（≤ 8MiB），不看文件实际大小；随后 `string()` 再拷一次 |
| G26 | 内存 | 低 | `transport/server.go:68` | WS 读上限常量意味着单连接最坏 ~97MiB 的读取缓冲（× 8 连接） |
| G27 | 结构 | 低 | 全仓 | if 嵌套分布：≤2 层占 89.1%，最深 7 层两处；整体是好的 |
| G28 | 优雅 | 中 | `presentation/presentation.go:192` | `funcMap` 覆盖内置 `printf`，只认 `%s`/`%/`：`%d` 输出字面量、非字符串 `%s` 静默变空 |
| G29 | 优雅 | 中 | `transport/server.go:245` | `serveUI` 381 行里同一段「渲染→报错→写回→return true」样板重复 16 次 |
| G30 | 优雅 | 低 | `sessions/index.go:19` | 三个无价值包装函数，没有任何测试替换它们 |

## 4. 逐项说明

### G01 巨型分发函数（高）

`dispatchCommon` 696 行、60 个 `case`，函数体内 22 处 `return map[string]any{…}`、14 处 `return map[string]bool{…}`。

```go
return map[string]bool{"queued": true}, nil
```

Effective Go 要求函数短小并聚焦单一职责；这类函数同时承担参数解码、worker 查找、业务调用与响应组装，任何一条命令的改动都要在近 700 行里定位。更实际的问题是**事实来源分裂**：方法清单已经有两处——`SupportedMethods`（`server.go:41`）与 `protocol.specs`（`internal/protocol/methods.go:59`）——分发 switch 是第三处，三者靠 `ui_contract_test.go` 静态核对彼此一致。

建议方向：按域拆成 `dispatchSession` / `dispatchFiles` / `dispatchConfig` / `dispatchTerminal` / `dispatchSessionOps`，各返回具名结构体；方法准入交给 `specs` 表。拆分本身不改变行为，可用现有 62 方法逐一回归。

### G02 构造函数长参数列表（中）

```go
func New(manager *run.Manager, store *sessions.Store, …, token, host string, publicOrigin PublicOrigin, ui *presentation.Renderer) *Server
```

13 个位置参数。同仓 `runtime.Config`、`terminal.Config`、`tunnel.Config` 都是配置结构体，只有这个构造函数是例外。风险很具体：`token` 与 `host` 相邻且同为 `string`，对调后编译通过、运行期表现为「鉴权永远失败」或「Host 校验永远拒绝」。

建议方向：引入 `transport.Config`，字段名与 `runtime.Config` 保持一致；`cmd/pi-bridge/main.go` 是唯一调用点（`main.go:179`），改动面小。

### G03 超长 HTTP 处理函数与字符串编码参数（中）

`serveUI`（`server.go:245`）381 行，把路由匹配、内容协商、鉴权后处理、片段渲染写在一个函数里；`ServeHTTP`（`:677`）126 行。同时 `encoding string` 作为参数穿过 11 个函数：

`fragment.go:19`、`presentation/compress.go:14`/`:72`/`:111`、`presentation/presentation.go:330`/`:363`/`:372`、`server.go:893`/`:909`/`:914`/`:2088`/`:2130`。

`encoding` 的取值只可能来自 `Accept-Encoding` 协商结果（`br`/`gzip`/空），当前以裸字符串在层间传递，任何一处拼错都不会被编译器发现。

建议方向：定义 `type encoding string` 与常量；内容协商与鉴权在中间件完成，处理函数从上下文取，而不是层层传参；`serveUI` 按路由段拆表（当前它已是一长串 `if`）。

### G04 错误链断裂（高）

三处证据：

1. `protocol.Error` 只有 `Code`/`Message` 两个字段，没有 cause，也没有 `Unwrap()`（`internal/protocol/protocol.go:30`）。调用方的 `errors.As` 只能抓到最外层包装，抓不到根因。
2. 全库仅 6 处 `%w`（`magiccontext/store.go:285/287/295`、`cmd/pi-relay/main.go:103`、`cmd/pi-bridge/main.go:170/212`），而 `protocol.E(…)` 有 400 处、`errors.As/Is` 37 处——比例说明绝大多数底层错误在转成协议错误时就丢了。
3. 拼接式包装：

```go
return PublicOrigin{}, errors.New("--public-origin 不是合法 URL：" + err.Error())
```

`url.Parse` 的 `*url.Error` 被拼进句子后，上层再也认不出根因；应写 `fmt.Errorf("--public-origin 不是合法 URL：%w", err)`。

同一模式在 `internal/management/discovery.go:202`/`:207` 更彻底——`err` 完全不进入新错误。这里**保留面向用户的中文提示是对的选择**（上游可能回显凭据，见同文件注释），缺的是同时保留 cause。

建议方向：`Error` 增加未导出 `cause error` 字段与 `Unwrap() error`，新增 `protocol.Wrap(code, message string, cause error) error`；未导出字段不影响 JSON 序列化与现有响应结构。随后把内部 `errors.New(句子 + 根因)`、`protocol.E(说明)` 之类的调用点按需改成 `Wrap`。

### G05 回执写入失败被静默丢弃（中）

```go
_ = s.receipts.Record(storage.Receipt{
```

两处（`internal/transport/claims.go:232`、`internal/transport/server.go:1113`）。回执是「命令结果未知/需要先对账」判定的持久依据（`protocol/methods.go:51` 注释写明重启后没有记录就无法判断是否执行过），写失败只在 `Receipts` 内部置 `degraded` 标记，外部无感知。

Code Review Comments 对「忽略错误」的立场是要么处理、要么在注释里说明为什么可以忽略；这里两者都没有。

建议方向：保留「不阻塞命令」的行为，但把降级暴露出去——`MetricsSink` 已有 `EventDropped` 之类的先例，可加 `ReceiptDegraded()`；或在 `/healthz` 增加只读位。

### G06 借用 `os.ErrClosed` 表意（低）

`internal/storage/receipts.go:325` 在存储未启用时返回 `os.ErrClosed`。`os.ErrClosed` 的语义是「文件已关闭」，用它表达「该功能未启用/已降级」会让 `errors.Is(err, os.ErrClosed)` 的调用方误解。

建议方向：自有哨兵错误（如 `ErrNotEnabled`），或让 `Record` 返回 `(recorded bool, err error)`。

### G07 字符串与裸布尔当枚举（中）

- `worker.status` 用字面量赋值 7 处：`runtime/manager.go:251`/`:563`/`:727`/`:754`/`:852`、`runtime/dialogs.go:82`/`:143`。
- `encoding string`（同 G03）。
- 裸布尔参数：`Stop(force bool)`、`Close(force bool)`（`terminal.go:467`）、`stop(force, idleOnly bool)`（`manager.go:738`，两个布尔相邻）、`signalGroup(cmd, force bool)`、`Bash(…, excludeFromContext bool)`。

Effective Go 建议用具名类型承载取值域；Code Review Comments 建议布尔参数改成具名方法或有意义的名字。`stop(force, idleOnly)` 的问题是调用点 `w.stop(force, false)` 读不出第二个参数的意图。

建议方向：`type status string` + 常量；`stop` 拆成 `stopForce`/`stopIfIdle`，或引入小 options 结构。

### G08 匿名容器穿透层边界（中）

典型签名：

```go
func (r *Renderer) RenderFiles(root string, entries []map[string]any, truncated bool) (string, error)
```

`entries` 的形状只在 `workspace` 内部定义，却以 `map[string]any` 跨到 `presentation`——拼错键名不会编译失败，只会渲染出空格。同类还有 `RenderDirs(path, parent string, dirs []map[string]string, …)`、`events.Ring.Stats() map[string]any`。全库 `any` 出现 299 次、`return map[string]…` 60 处。

部分返回已有具名类型（`runtime.Info`、`runtime.State`），说明模式本身是清楚的，只是没贯彻。

建议方向：渲染层需要的行数据在 `presentation` 内定义包级结构体并由 `transport` 负责把 `workspace`/`sessions` 的类型映射过去；`Stats` 换成结构体。这两步都不改协议，只改 Go 内部边界。

### G09 presentation 的导出面（中）

`internal/presentation` 87 个导出符号、21 个 `Render*` 方法，全部由 `transport` 使用。Go 的惯例是**接口定义在使用方**：`transport` 定义它真正调用的那组方法的小接口，既方便测试替身，也让 presentation 不必为「谁在用我」而设计。

`runtime.MetricsSink`（`manager.go:102`）与 `tunnel.Handler`（`client.go:51`）已经是这个模式，presentation 是例外。

### G10 配额等待用轮询表达（低）

```go
case <-time.After(2 * time.Millisecond):
```

`internal/transport/send_queue.go:33`，用 CAS + 2ms 轮询等待字节配额。先澄清一个常见误判：**Go 1.23 起未被引用的定时器可被 GC 回收**，所以这里不是定时器泄漏（`time` 包文档已更新此说明）。问题是用轮询代替通知：等待方每 2ms 唤醒一次，配额释放没有触发唤醒。

建议方向：发送协程归还配额时通过带缓冲 channel 通知，或直接用 `sync.Cond`／把配额与入队合并成一个受互斥保护的队列。这块是背压修复（10133 段）的核心，改动前需要先跑 `replay_ws_integration_test.go` 与 `-race`。

### G11 双锁纪律未注释（低）

`Manager` 有 `mu` 与 `startMu`（`runtime/manager.go:91`），`Files` 有 `mu` 与 `indexMu`（`workspace/files.go:41`），`Renderer` 的 `mu` 同时保护 templates/assets/compressed/mc。当前 `-race` 全绿，属于可维护性问题：读代码的人无法判断「哪个字段受哪把锁保护」「能不能在持 A 时取 B」。

建议方向：每个 mutex 上方一句注释写明保护范围与加锁顺序（现有代码里 `w.mu` 已有类似注释，是好的先例）。

### G12 nil 接收者与 nil 检查混用（中）

三种做法同时存在：

- `SetMagicContext` 容忍 nil 接收者（`presentation.go:64`）；
- `ServeHTTP`/`serveUI` 用 `if s.ui == nil` 显式守卫（`server.go:258`/`:720`）；
- `fragmentIssue` 直接调用 `s.ui.RenderNote`（`fragment.go:27`），其安全性完全依赖调用点都先做过守卫。

nil 安全只覆盖一个方法，等于把「记得判空」的责任交给未来每个调用点；漏一处就是 panic。

建议方向：二选一并写进包注释。倾向「UI 层显式判空」：`transport` 已经有两处守卫，补上 `fragmentIssue` 入口判断即可，同时删掉 `SetMagicContext` 的 nil 接收者特例（`transport.New` 改为条件注入）。

### G13 假引用（低）

```go
var _ = json.Marshal          // internal/tunnel/client.go:270
var _ = pty.Winsize{}         // internal/terminal/proc_oth.go:13
```

第一处让 `encoding/json` 仅为这一行而导入——该文件其余部分不使用 json。第二处更明显多余：下一行函数签名已经使用了 `*pty.Winsize`。Go 编译器拒绝未使用导入，正是为了避免这类噪音。

建议方向：删除。若要保留编译期断言，应写成有意义的断言（例如 `var _ io.Writer = (*Client)(nil)`）。

### G14 无人使用的测试接缝（低）

```go
// Now 供测试替换时间来源。
var Now = time.Now            // internal/presentation/presentation.go:995
```

全库没有任何引用点，也没有测试替换它——既是死代码，也是唯一的全局可变状态（同样的意图在别的包是靠参数传入）。

建议方向：删除；将来确实需要注入时间时用参数或 `Clock` 接口。

### G15 不可达分支（低）

`events.Ring.Push`（`internal/events/ring.go:44`）：

```go
for len(r.items) > r.maxItems || r.bytes > r.maxBytes {
    if len(r.items) == 1 && r.bytes <= r.maxBytes {   // 不可达
        break
    }
    if len(r.items) == 1 {
        r.dropped++
        break
    }
```

进入循环体时 `len==1` 只可能因为 `bytes > maxBytes`：`maxItems` 由 `NewRing` 兜底为 256，`len > maxItems` 不可能在 `len==1` 时成立。所以第一个分支恒假。

建议方向：删掉第一个分支，保留带注释的「单条超限也保留并记 dropped」分支。

### G16 缺包注释（低）

18 个包中 17 个有 `// Package xxx` 注释，只有 `internal/transport` 没有。`go.dev/doc/comment` 要求包注释以 `Package 包名` 开头，`go doc` 才会把它显示为包说明。

建议方向：补一段——这个包负责接入、鉴权、限额与命令分发，并把「WebSocket 连接与隧道虚拟连接共用一份分发实现」这个关键设计放进注释。

### G17 测试细节（低）

- 417 个测试函数里 386 个（92%）名称含中文，如 `TestDecode拒绝未知字段与多值`。中文注释符合项目规则，但**标识符**用中文会让 `go test -run` 必须输入中文，CI 过滤、编辑器跳转、外部工具链都更别扭。官方没有硬性规定，属建议：函数名用英文，中文留在 `t.Run` 子测试名或注释。
- `internal/terminal/terminal_test.go:163` 用 `os.Setenv` + `defer os.Setenv` 恢复，应换成 `t.Setenv`（Go 1.17+，自动恢复且禁止并行）。
- **正面结论**：测试里没有在 goroutine 内调用 `t.Fatal`。我用括号配对脚本核对了 6 个疑似点，全部是「`go func` 一行 + 主 goroutine 里 `select`」的误报。这类问题在这个仓库里不存在。

### G18 缺少静态检查器（中）

CI（`.github/workflows/check.yml`）只跑 `gofmt -l`、`go vet ./...`、`go test -race -count=1 ./...`。G13、G14、G15 都能在这套检查下存活，说明覆盖有缺口：G14（包级未使用变量）确定在 staticcheck `unused` 的默认检测范围，G13/G15 是否也能被自动发现需要实际跑一遍确认（本次环境未安装这两个工具，所以不给结论）。

建议方向：接入 staticcheck（或 golangci-lint 固定规则集），先不加 `-fail` 跑一遍看完整基线，再挑零误报的规则进 CI。这一步能防止同类问题再被写进来，价值高于逐个手改；同时也会暴露本文件未列到的同类问题。

### G19 magic-context 的 SQL 构造（低）

`internal/magiccontext` 通过 `sqlite3` 命令行执行只读查询，参数经校验后代回 SQL 字符串（sqlite3 CLI 不支持绑定参数）。这与 Go 惯用的 `database/sql` 相反，但它是**为不加 SQLite 驱动而做的显式取舍**，且已配套沙箱与白名单校验。本轮只登记，不建议在本批改动。

## 5. 第二轮：内存、嵌套与重复

### 5.1 怎么取的数

- **if 嵌套**：先写了一版剥离字符串与注释的脚本，但它**在块注释里丢掉了换行**，导致行号整体偏移（同一处先报 7 层、换个写法报 2 层）。第二版改成**保长度、保换行**的剥离：被移除的字符一律换成空格，`\n` 原样保留，并加了一条自检——每个文件剥离后花括号收支必须为 0（仓库内全部非测试文件均通过）。下文行号都用第二版，可直接 `sed` 复核。
- **分配**：`go test -bench ... -benchmem` 取每操作数据，`-memprofile` + `go tool pprof -top/-list -sample_index=alloc_space` 定位到行。为了让被测代码而不是夹具主导采样，采样时把迭代次数调到 3000，夹具的一次性开销因此可忽略。
- **转换成本**：`toAnyMaps` 的 500 项数据用一个临时基准测得（`internal/transport/zz_tmp_bench_test.go`），**测完即删**，未进提交。
- **printf 行为**：用临时 in-package 测试把同一模板分别用 `funcMap()` 与内置 `printf` 渲染，直接对比输出（同样测完即删）。

### 5.2 关键数据

首页（`BenchmarkHistory首页/100轮`，单页约 0.7MB 会话；3000 次采样）：

| 指标 | 值 |
|---|---|
| 时间 / 分配 | ~306µs，139KB/op，133 allocs/op |
| `make([]byte, node.size)`（`store.go:343`） | 287.53MB / 3000 ≈ **96KB/op**（占该函数 flat 分配的 96%） |
| `alignToTurn`（`store.go:429`，cum） | 92.97MB / 3000 ≈ **31KB/op** |
| 其中 `ProjectEntries`（cum，经 `entryKind`） | 72.86MB / 3000 ≈ 24KB/op |
| `readEntry`（flat） | 20.11MB / 3000 ≈ 6.7KB/op |
| `jsontext.(*Value).UnmarshalJSON` | 63.36MB / 3000 ≈ 21KB/op |
| 与文件规模的无关性 | 100/500/2000 轮的首页耗时均 ~306–311µs、133–134 allocs/op，说明索引缓存在起作用 |

渲染（`BenchmarkRenderHistory`，3000 次）：

| 指标 | 值 |
|---|---|
| 时间 / 分配 | ~323µs，154KB/op，1664 allocs/op |
| `strings.Builder.Write` | 135.47MB / 3000 ≈ 45KB/op（输出的 HTML 本身） |
| `ProjectEntries`（cum） | 218.29MB / 3000 ≈ **73KB/op** |
| 其中 `scanLazyBlocks` | 28.5MB flat、64.51MB cum ≈ 21KB/op |
| 错误对象构造（`transformUnmarshalError` + `newUnmarshalErrorAfter`） | 27MB / 3000 ≈ **9KB/op**，全部来自「期望内失败」 |

`toAnyMaps`（500 项）：**589,745 ns/op，359,921 B/op，7,017 allocs/op**。对照一次完整历史页渲染约 323µs / 154KB——列一次 500 项目录的转换成本约等于渲染两个历史页。

嵌套分布（1,798 个 `if`）：

| 深度 | 数量 | 占比 |
|---|---|---|
| 1 | 1035 | 57.6% |
| 2 | 566 | 31.5% |
| 3 | 148 | 8.2% |
| 4 | 36 | 2.0% |
| 5 | 9 | 0.5% |
| 6 | 2 | 0.1% |
| 7 | 2 | 0.1% |

深度 ≥5 的全部 13 处：`sessions/metadata.go:67`、`:69`（titleForPage 的串/数组回落）、`presentation/presentation.go:200`、`:201`（funcMap 的 printf）、`runtime/manager.go:322`（reap 的指标判空）、`transport/server.go:366`、`:1630`、`management/config_values.go:96`、`:103`（密钥/占位符判定）、`management/discovery.go:145`（input 数组解析）、`management/packages.go:122`、`:128`、`:132`（并发查询结果归类）。

### 5.3 逐项

**G20 只为判断 kind 的整条读盘与投影（高）**

`alignToTurn`（`store.go:429`）把本页最旧一条以及向前补取的每一条都交给 `readEntry` + `entryKind`；`entryKind` 又调 `ProjectEntries`（完整 `json.Unmarshal` 成 `Entry`）——而它只想知道「这条是不是 user」。同一信息，`scanFile` 扫描时**已经解析过行首**（`type`/`id`/`parentId` 的快路径，见 `sessions/scan.go:20`）。

修法：扫描时把「是不是 user 锚点」存进 `node`，`alignToTurn` 直接查表，不再读盘也不反序列化。预期消掉 ≈31KB/op 与相应分配次数（139KB/op 的约 22%）。两个注意点：`node` 变大后 `maxCachedBytes`（16MB）的估算要重算；轮边界对齐有既有回归，改完必须复跑。

**G21 用失败做类型判断（中）**

```55:55:internal/sessions/lazy.go
	if json.Unmarshal(msg.Content, &blocks) != nil {
```

`content` 是纯文本（字符串）时这次 unmarshal **必然失败**；`flattenContent`（`search.go:208`）则相反：对数组内容先试字符串、失败一次再试数组。两处都把「失败」当成类型判断，而 JSON 解码失败会构造带位置的错误对象——渲染基准里这类对象共 ~9KB/op。

修法：看 `content` 的第一个非空白字节（`"` 是字符串、`[` 是数组），选好再解析。行为不变、收益确定、风险极低。

**G22 每页 N 次分配与缺失的预分配（中）**

`store.go:343` 每条条目一次 `make([]byte, node.size)`。这 96KB/op 的**字节数本身是必要的**（`Page.Entries` 要活到渲染完），可省的是**分配次数**：一次 `make` 出总长，用 `ReadAt` 逐条填进对应切片。同一函数里 `selected := []string{}`（`:321`）与 `Entries: []json.RawMessage{}`（`:340`）在长度可知（`len(selected)`）的情况下没有预分配，会按倍增重分配并拷贝。

**G23 每条记录一次分配（低）**

`jsonl.Read` 每次都 `append` 到 `nil` 切片，因此每条记录都新分配一块。它被扫描（`scan.go:21`）、搜索（`search.go:108`）、惰性加载（`lazy.go:198`）、元数据（`metadata.go:26`）与 Pi 读循环（`pi/client.go:321`）共用。

可以加「调用方提供缓冲」的变体，但**返回切片不得跨调用保留**——需逐个调用点确认（扫描与 RPC 读循环都只当场解析后丢弃，理论上可行）。这条不要顺手改，属于「有收益但要小心」的一类。

**G24 喂渲染层的 JSON 往返（中）**

```846:846:internal/transport/server.go
func toAnyMaps(v any) []map[string]any {
```

调用点在 `:423`（包清单）、`:444`（文件列表）、`:483`（搜索结果）。类型化切片 → `json.Marshal` → `json.Unmarshal` 成 `[]map[string]any` → 渲染层再用 `stringField(p, "name")` 之类的按键取值拼回**强类型行结构**（`FileRow`/`PackageRow`）。就是为了回到类型，先绕了一圈 JSON。

模板用的是 `{{.Name}}`/`{{.Path}}` 这类**字段名**，不吃 JSON 键名，所以让渲染层直接收 `[]presentation.FileRow`（由 `transport` 从 `workspace.Entry` 逐字段转换）即可整段删掉往返。`RenderDirs` 已证明可行：它收 `[]map[string]string` 后第一件事就是转成 `[]DirRow`。

与 G08 一起做：改的是 Go 内部签名，不动协议。改前先核对三个模板用到的字段名（`files.html`、`packages.html`、`search.html`）。

**G25 bash 输出按上限预分配（低）**

```90:90:internal/runtime/bash.go
	buf := make([]byte, maxBytes)
```

`maxBytes` 由请求给出、上限 8MiB；文件只有几百字节时也分配这么多，随后 `string(buf[:n])` 再复制一次。改成 `want := min(maxBytes, st.Size())` 就够。

**G26 WS 读上限隐含的内存上界（低）**

`wsReadLimit = 8 × 12MiB + 1MiB ≈ 97MiB`（为容纳 8 张附件的 base64）。`coder/websocket` 会为整条消息分配缓冲，所以**单连接最坏约 97MiB**，连接槽上限 8 → 最坏约 776MiB。这是显式取舍（附件必须能过），但值得写进文档，避免以后有人以为是 KB 级。

**G27 嵌套深度（低，结论偏正面）**

≤2 层占 89.1%、≤3 层占 97.1%，且 `} else {` 全仓仅 22 处 / 16.7k 行——早返回风格是贯彻了的。真正值得动的只有两处 7 层：`funcMap` 的手写 printf（G28）与 `titleForPage` 的「先试字符串再试数组」（与 G21 同源，一起改最划算）。其余 5 层多为「并发任务归类」的合理结构，不建议为降层数而重构。

**G28 被覆盖的内置 `printf`（中）**

`funcMap()` 只注册了一个函数，恰好与 `text/template` 的**内置**函数同名——内置实现因此被换成一个只支持 `%s` 与 `%/` 的版本。实测（同一模板、同为 `html/template`）：

| 模板 | 自定义 printf | 内置 printf |
|---|---|---|
| `printf "%d" 42` | `%d`（字面量） | `42` |
| `printf "%s/%s" "prov" "model"` | `prov/model` | `prov/model` |
| `printf "%s" 7` | 空字符串 | `%!s(int=7)` |
| `printf "%%"` | `%%` | `%` |

今天没有线上错误：全部模板里只有 `pi-webui-htmx/src/templates/models.html:2` 用了一处 `printf "%s/%s"`，两个参数都是字符串。风险是**静默**的——内置实现遇到类型不符会打出 `%!s(int=7)` 这种显眼标记，自定义版直接输出空。

建议：要么改名成 `joinSlash`/`key` 这样诚实的名字（只服务那一个用途），要么遇到不认识的动词时回落 `fmt.Sprintf`。不要保留一个「看起来像 fmt」的半实现。

**G29 serveUI 的重复样板（中）**

`serveUI`（381 行）里 `s.fragmentIssue(w, encoding, …)` 出现 25 次、`writeHTML(w, encoding, html)` 16 次，同一段「渲染 → 失败渲染说明 → 写回 → `return true`」重复 16 次。抽一个 `serveFragment(w, encoding, func() (string, error)) bool` 就能消掉，同时把 `serveUI` 从 381 行降下来。与 G03 是同处代码的两个视角，一起做。

**G30 无价值的包装函数（低）**

```19:19:internal/sessions/index.go
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
```

同一文件还有 `newBufReader(f *os.File) *bufio.Reader { return bufio.NewReader(f) }` 与 `fnvNew64a() *fnv64a { return &fnv64a{} }`。三者都只有一处调用点、没有任何测试替换它们——像是为「将来可替换」预留的缝，但缝没接上东西。要么删掉，要么在注释里写清为何保留。

### 5.4 第二轮的正向结论

| 项 | 数据 |
|---|---|
| 接收者命名一致性 | 同一类型多个接收者名：**0 处** |
| 早返回风格 | `} else {` 仅 22 处 / 16.7k 行；嵌套 ≤3 层占 97.1% |
| 缓存有界 | `scanCache` 单文件 + 16MB 上界 + dev/ino 校验；`workspace.indexCache` 有 TTL 与条数上限 |
| 队列有界 | Pi 客户端在途命令 64、通知队列独立有界、订阅 32 条/1MiB、事件环 256 条/1MiB、终端 4 个、对话 16 个 |
| 并发原语克制 | `Mutex/WaitGroup/Pool` 共 27 处，无 `sync.Map`，包级可变状态只有 `presentation.Now`（已在 G14 登记） |
| 缓存收益可测 | 2000 轮会话首页 306µs（与 100 轮同量级） |

### 5.5 只说不做的候选

- **G22 的一体化缓冲**与 **G23 的复用缓冲**都涉及「切片所有权」约定变更，建议各自单独提交，并配 `-race` 与首页/翻页基准对比。
- **G20 的 node 扩字段**会让 `maxCachedBytes` 的字节估算偏乐观，改完应重测一次 2000 轮会话的常驻内存。
- 本轮**没做**长时间稳定性观测（如 8 连接持续压力下的 RSS 曲线），也没在 CI 里加内存回归。若要锁住 G21/G22/G24 的收益，最省事的是把已有基准加一条 `-benchmem` 上限断言。

## 6. 明确不算问题（避免后续误改）

| 现象 | 为什么不改 |
|---|---|
| 中文错误文本与中文注释 | 项目规则 #227 明确要求；对外提示走 `protocol.Error.Message` |
| `requestId` / `deviceId` 等 camelCase | 协议 wire format，不是 Go 标识符 |
| `time.After` 出现在循环与 `select` | Go 1.23 起未引用定时器可被 GC；不是泄漏（`time` 文档） |
| 清理路径 `_ = w.Stop(true)`、`_ = t.Close(true)` | 进程/连接即将关闭，符合「无法处理的错误不强行处理」；与 G05 的区别是回执是对账依据 |
| 无 `panic`、`goto`、`reflect`、`ioutil` | 符合 Effective Go |
| `crypto/rand` + `subtle.ConstantTimeCompare` | 安全路径正确；未使用 `math/rand` |
| `context` 首参、不入结构体；`http.Server` 超时齐全 | 符合 Code Review Comments |
| 测试未在 goroutine 内 `t.Fatal` | 已用脚本核对，6 个疑似点均为误报 |

## 7. 建议的修复顺序

按「风险低 → 风险高」排，前两步可以立刻做且几乎无回归风险：

1. **零风险清理**（G13、G14、G15、G16、G17 的 `t.Setenv`、G30）：删假引用、死代码与无价值包装、补包注释。一个提交。
2. **接入 staticcheck**（G18）：先看基线，再定规则。它可能再报出本文件没列到的同类问题。
3. **错误链**（G04）：`Error` 加 cause 与 `Unwrap`、新增 `Wrap`；先只加能力不改调用点，再逐包迁移。需要回归的错误路径：`public_origin` 解析、`discovery` 出站、`magiccontext` 存储不可用。
4. **回执可观测性**（G05、G06）：加降级计数/健康位，`Record` 语义收敛。
5. **类型表达**（G07、G08）：`status`/`encoding` 具名类型、`stop(force, idleOnly)` 拆分、渲染边界结构体。面较大，建议按包分批。
6. **结构性重构**（G01、G02、G03、G09、G12）：分发拆分、`transport.Config`、`serveUI` 拆表、nil 语义统一。这批动的是主干，建议单独排期，并在开始前先补齐「62 个方法逐一调用」的契约回归——现有 `ui_contract_test.go` 与 `methods_test.go` 是基础。
7. **G10（并发配额通知）**：与 G01 同批或独立，改动前先跑补发环联测与 `-race`。
8. **第二轮内存项**（G20→G24）：按收益/风险比排——先 G21（首字节判断，几乎无风险）与 G24（与 G08 同批做），再 G20（node 存 kind，需重估缓存上界），最后 G22/G23（涉切片所有权，各自单独提交）。
9. **G25–G29**：G25 一行改；G28 要么改名要么回落 `fmt.Sprintf`；G29 与 G03 同批。

每步的验收沿用仓库既有门槛：`test -z "$(gofmt -l .)"`、`go vet ./...`、`go test -race -count=1 ./...`，跨仓改动再跑 `scripts/verify-pair.sh`。

## 8. 与既有台账的关系

`docs/code-audit.md` 维护 B01–B81 与 U01–U21 两组编号，内容偏**正确性与安全**；本文件用独立前缀 G，记录的是**语言惯用性与结构**，两者可能落在同一段代码上但视角不同，不重复立号。

落修前应先查 `code-audit.md`：若某个 G 项与既有 B/U 项指向同一处，应合并到那一条修复里一次改完，避免同一段代码被两次重构。本文件未做这项比对。
