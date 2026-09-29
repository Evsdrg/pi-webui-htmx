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

没做的：本机未安装 staticcheck/golangci-lint，因此下文 G18 只给建议未给基线；未做性能基准以外的长时间内存观测；未逐条比对 B/U 台账（落修前应先查重，见第 9 节）。

引用约定：第 3 节表格给出每个问题的**全路径**；正文为可读性改用文件名简写（`store.go:343`），同名的文件靠包名与上下文区分（如 `sessions/store.go` 与 `magiccontext/store.go`）。文中所有行号均按 `863041d`/`f1e95da` 的源码核对过。

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

### G01 巨型分发函数（高） · 已在批次 K1（结构）+ K2（类型）修复

`dispatchCommon` 696 行、60 个 `case`，函数体内 22 处 `return map[string]any{…}`、14 处 `return map[string]bool{…}`。

```go
return map[string]bool{"queued": true}, nil
```

Effective Go 要求函数短小并聚焦单一职责；这类函数同时承担参数解码、worker 查找、业务调用与响应组装，任何一条命令的改动都要在近 700 行里定位。更实际的问题是**事实来源分裂**：方法清单已经有两处——`SupportedMethods`（`server.go:41`）与 `protocol.specs`（`internal/protocol/methods.go:59`）——分发 switch 是第三处，三者靠 `ui_contract_test.go` 静态核对彼此一致。

建议方向：按域拆成 `dispatchSession` / `dispatchFiles` / `dispatchConfig` / `dispatchTerminal` / `dispatchSessionOps`，各返回具名结构体；方法准入交给 `specs` 表。拆分本身不改变行为，可用现有 62 方法逐一回归。

### G02 构造函数长参数列表（中） · 已在批次 I 修复（见第 10 节）

```go
func New(manager *run.Manager, store *sessions.Store, …, token, host string, publicOrigin PublicOrigin, ui *presentation.Renderer) *Server
```

13 个位置参数。同仓 `runtime.Config`、`terminal.Config`、`tunnel.Config` 都是配置结构体，只有这个构造函数是例外。风险很具体：`token` 与 `host` 相邻且同为 `string`，对调后编译通过、运行期表现为「鉴权永远失败」或「Host 校验永远拒绝」。

建议方向：引入 `transport.Config`，字段名与 `runtime.Config` 保持一致；`cmd/pi-bridge/main.go` 是唯一调用点（`main.go:179`），改动面小。

### G03 超长 HTTP 处理函数与字符串编码参数（中） · 已在批次 J 修复（见第 10 节）

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

### G07 字符串与裸布尔当枚举（中） · 已在批次 I 修复（`encoding` 一项在批次 J）

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

### G10 配额等待用轮询表达（低） · 已在批次 L 修复，见第 10 节

```go
case <-time.After(2 * time.Millisecond):
```

`internal/transport/send_queue.go:33`，用 CAS + 2ms 轮询等待字节配额。先澄清一个常见误判：**Go 1.23 起未被引用的定时器可被 GC 回收**，所以这里不是定时器泄漏（`time` 包文档已更新此说明）。问题是用轮询代替通知：等待方每 2ms 唤醒一次，配额释放没有触发唤醒。

建议方向：发送协程归还配额时通过带缓冲 channel 通知，或直接用 `sync.Cond`／把配额与入队合并成一个受互斥保护的队列。这块是背压修复的核心，改动前需要先跑 `replay_ws_test.go`（穿过 fake Pi → 补发环 → WS 的联测）与 `-race`。

### G11 双锁纪律未注释（低）

`Manager` 有 `mu` 与 `startMu`（`runtime/manager.go:91`），`Files` 有 `mu` 与 `indexMu`（`workspace/files.go:41`），`Renderer` 的 `mu` 同时保护 templates/assets/compressed/mc。当前 `-race` 全绿，属于可维护性问题：读代码的人无法判断「哪个字段受哪把锁保护」「能不能在持 A 时取 B」。

建议方向：每个 mutex 上方一句注释写明保护范围与加锁顺序（现有代码里 `w.mu` 已有类似注释，是好的先例）。

### G12 nil 接收者与 nil 检查混用（中） · 已在批次 I 修复（见第 10 节）

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

### G19 magic-context 的 SQL 构造（低） · 本批只登记，见第 10 节

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
| `make([]byte, node.size)`（`internal/sessions/store.go:343`） | 287.53MB / 3000 ≈ **96KB/op**（占该函数 flat 分配的 96%） |
| `alignToTurn`（`internal/sessions/store.go:429`，cum） | 92.97MB / 3000 ≈ **31KB/op** |
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

深度 ≥5 的全部 13 处：`sessions/metadata.go:67`、`:69`（titleForPage 的串/数组回落，两个尝试分别在 `:58` 与 `:65`）、`presentation/presentation.go:200`、`:201`（funcMap 的 printf）、`runtime/manager.go:322`（reap 的指标判空）、`transport/server.go:366`、`:1630`、`management/config_values.go:96`、`:103`（密钥/占位符判定）、`management/discovery.go:145`（input 数组解析）、`management/packages.go:122`、`:128`、`:132`（并发查询结果归类）。

### 5.3 逐项

**G20 只为判断 kind 的整条读盘与投影（高）** —— 已在批次 F 修复，见第 10 节。

`alignToTurn`（`store.go:429`）把本页最旧一条以及向前补取的每一条都交给 `readEntry` + `entryKind`；`entryKind` 又调 `ProjectEntries`（完整 `json.Unmarshal` 成 `Entry`）——而它只想知道「这条是不是 user」。同一信息，`scanFile` 扫描时**已经解析过行首**（`type`/`id`/`parentId` 的快路径，见 `sessions/scan.go:20`）。

修法：扫描时把「是不是 user 锚点」存进 `node`，`alignToTurn` 直接查表，不再读盘也不反序列化。预期消掉 ≈31KB/op 与相应分配次数（139KB/op 的约 22%）。三个约束（详见 §6.3）：

1. `isUser` 需要 `message.role`，而快路径只读到 parentId；增量是「顺手解出 role」（该路径本来就已整行扫过一遍做 `balancedJSON` 校验），不是整行重解析。
2. 「扫描期顺手存派生信息」已有先例：`node.lastModelID` 就是这么来的（`scan.go:82-87`、`:116-121`）。
3. 去掉读盘**不会**丢掉「读期间文件变化」的检测（页循环仍逐条 `ReadAt` + `json.Valid`），但失败形态会从「页更短」变成 `conflict`；另外 `node` 变大后 `maxCachedBytes`（16MB）的估算要重算。

**G21 用失败做类型判断（中）** —— 已在批次 D 修复，见第 10 节。

```55:55:internal/sessions/lazy.go
	if json.Unmarshal(msg.Content, &blocks) != nil {
```

`content` 是纯文本（字符串）时这次 unmarshal **必然失败**；`flattenContent`（`search.go:208`）则相反：对数组内容先试字符串、失败一次再试数组；`metadata.go:58/65` 是第三份同样的写法。三处都把「失败」当成类型判断，而 JSON 解码失败会构造带位置的错误对象——渲染基准里这类对象共 ~9KB/op。

修法：看 `content` 的第一个非空白字节（`"` 是字符串、`[` 是数组），选好再解析。行为不变、收益确定、风险极低。

**G22 每页 N 次分配与缺失的预分配（中）** —— 已在批次 G1 修复，见第 10 节。

`store.go:343` 每条条目一次 `make([]byte, node.size)`。这 96KB/op 的**字节数本身是必要的**（`Page.Entries` 要活到渲染完），可省的是**分配次数**：一次 `make` 出总长，用 `ReadAt` 逐条填进对应切片。同一函数里 `selected := []string{}`（`:321`）与 `Entries: []json.RawMessage{}`（`:340`）在长度可知（`len(selected)`）的情况下没有预分配，会按倍增重分配并拷贝。

**G23 每条记录一次分配（低）** —— 已在批次 G2 修复（受限范围），见第 10 节。

`jsonl.Read` 每次都 `append` 到 `nil` 切片，因此每条记录都新分配一块。它被扫描（`scan.go:30`）、搜索（`search.go:122`）、惰性加载（`lazy.go:204`）、元数据（`metadata.go:32`）、会话索引（`index.go:390`）与 Pi 读循环（`pi/client.go:323`）共用。

**这条不能一刀切**：`pi/client.go` 把读到的字节向上交出（`json.RawMessage(b)` → `runtime/manager.go:585` 存进 `pendingDialogs`，以及响应帧的 `frame.Data`），而读循环立刻去读下一帧——复用缓冲会静默覆写它们。可安全改的只有不保留字节的扫描类调用点，安全/危险清单见 §6.1。

**G24 喂渲染层的 JSON 往返（中）** —— 已在批次 E 修复，见第 10 节。

```846:846:internal/transport/server.go
func toAnyMaps(v any) []map[string]any {
```

调用点在 `:423`（包清单）、`:444`（文件列表）、`:483`（搜索结果）；加上 `RenderDirs` 与 `RenderGitStatus`，共 5 个吃 map 的入口。类型化切片 → `json.Marshal` → `json.Unmarshal` 成 `[]map[string]any` → 渲染层再用 `stringField(p, "name")` 之类的按键取值拼回**强类型行结构**（`FileRow`/`PackageRow`/`SearchHit`）。就是为了回到类型，先绕了一圈 JSON。

模板用的是 `{{.Name}}`/`{{.Path}}` 这类**字段名**（已逐字核对：`files.html` 用 `Name/Path/IsDir/Size`，`packages.html` 用 `Name/Source/Version/Latest/HasUpdate/Disabled/Error`，`search.html` 用 `SessionID/EntryID/Title/Cwd/Snippet`），不吃 JSON 键名，所以让渲染层直接收类型化行即可整段删掉往返。

与 G08 一起做：改的是 Go 内部签名，不动协议。代价与两个方案见 §6.6；注意 `stringField`/`boolField`/`recordOf` 还被模型面板、统计、分支树使用，**不能连带删除**。

**G25 bash 输出按上限预分配（低）** · 已在批次 N1 修复，见第 10 节

```90:90:internal/runtime/bash.go
	buf := make([]byte, maxBytes)
```

`maxBytes` 由请求给出、上限 8MiB；文件只有几百字节时也分配这么多，随后 `string(buf[:n])` 再复制一次。改成 `want := min(maxBytes, st.Size())` 就够。

**G26 WS 读上限隐含的内存上界（低）** · 本批只登记，见第 10 节

`wsReadLimit = 8 × 12MiB + 1MiB ≈ 97MiB`（为容纳 8 张附件的 base64）。`coder/websocket` 会为整条消息分配缓冲，所以**单连接最坏约 97MiB**，连接槽上限 8 → 最坏约 776MiB。这是显式取舍（附件必须能过），但值得写进文档，避免以后有人以为是 KB 级。

**G27 嵌套深度（低，结论偏正面）** —— 两处 7 层已在批次 D 处理，见第 10 节。

≤2 层占 89.1%、≤3 层占 97.1%，且 `} else {` 全仓仅 22 处 / 16.7k 行——早返回风格是贯彻了的。真正值得动的只有两处 7 层：`funcMap` 的手写 printf（G28）与 `titleForPage` 的「先试字符串再试数组」（与 G21 同源）——两处都已在批次 D/H 处理。其余 5 层多为「并发任务归类」的合理结构，不建议为降层数而重构。

**G28 被覆盖的内置 `printf`（中）** —— 已在批次 H 修复，见第 10 节。

`funcMap()` 只注册了一个函数，恰好与 `text/template` 的**内置**函数同名——内置实现因此被换成一个只支持 `%s` 与 `%/` 的版本。实测（同一模板、同为 `html/template`）：

| 模板 | 自定义 printf | 内置 printf |
|---|---|---|
| `printf "%d" 42` | `%d`（字面量） | `42` |
| `printf "%s/%s" "prov" "model"` | `prov/model` | `prov/model` |
| `printf "%s" 7` | 空字符串 | `%!s(int=7)` |
| `printf "%%"` | `%%` | `%` |

今天没有线上错误：全部模板里只有 `pi-webui-htmx/src/templates/models.html:2` 用了一处 `printf "%s/%s"`，两个参数都是字符串；另外 `%/` 在全部模板里**没有任何使用**。风险是**静默**的——内置实现遇到类型不符会打出 `%!s(int=7)` 这种显眼标记，自定义版直接输出空。

建议：**优先选零跨仓的修法**——删掉自定义覆盖，直接用内置 `printf`（`%s/%s` 行为一致）。改名成 `joinSlash`/`key` 属于跨仓原子改动，顺序要求与陷阱见 §6.5。

**G29 serveUI 的重复样板（中）** · 已在批次 J 修复，助手名为 `renderFragment`（见第 10 节）

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

## 6. 交叉影响：谁和谁能一起改、谁会把谁改坏

这一节是落地前的约束清单。下列判断都基于源码事实（行号已核对），不是推测。

### 6.1 三组会互相破坏的组合

**(1) G23（复用读取缓冲） × 事件与响应的所有权 —— 会静默损坏数据（最危险）**

数据流是这样的：

```
pi/client.go:323   b, _, err := jsonl.Read(r, c.maxFrame)
pi/client.go:354   rr := result{data: frame.Data}          // frame.Data 是 b 的子切片，交给等待中的 goroutine
pi/client.go:365   c.onEvent(json.RawMessage(b))           // 同一个 b 继续向上传
runtime/manager.go:585   w.pendingDialogs[ev.ID] = raw     // 原样保留，直到用户回答对话框
dialogs.go:29/76         再把 raw 读出来使用（:113 是重新登记）
```

而读循环在交出 `b` 后**立刻**去读下一帧。一旦 `jsonl.Read` 改成复用缓冲，扩展对话框的内容与 RPC 响应数据都会被后续帧覆写——这正是补发队列修复里同一类「看起来没事」的别名缺陷，而且 `-race` 发现不了（它是逻辑所有权问题，不是数据竞争）。

安全范围与危险范围：

| 调用点 | 是否保留 `b` | 能否复用缓冲 |
|---|---|---|
| `sessions/scan.go:30` | 否，只存 offset/size | 可以 |
| `sessions/index.go:390` | 否，只读首行 | 可以 |
| `sessions/search.go:122`、`sessions/metadata.go:32` | 否，只产出 string | 可以 |
| `sessions/lazy.go:204` | 需逐端点确认（thinking/image 当场解码；`session.entries` 会把原始条目带出） | 待审 |
| `pi/client.go:323` | **是**（响应 data + pendingDialogs） | **不可以** |

补一句：`events.Ring.Push` 存的是 `json.Marshal` 的新字节（`manager.go:517`），所以补发环本身与 `jsonl.Read` 无关；风险全在 `pi/client` 那条路径上。

**(2) G04（加 `Unwrap`） × 37 处 `errors.As/Is` —— 用错误码决定行为的地方会改道**

加 `Unwrap` 后 `errors.As` 会穿到更深层。三个已知的码驱动点：

- `transport/server.go:359-372` 的 204 分支：要求 history 报 `not_found` **且** `store.Find` 报 `not_found` **且** 该身份仍有活跃 worker。若迁移时把某个底层失败（权限、IO）包成 `not_found`，这个分支会开始吞掉真正的错误，把故障伪装成「分支尚未落盘」。
- `claims.go:183-232`：`outcome_unknown` / `conflict` / `busy` 决定「能不能重试」，直接关系 #258「结果未知不重发」。
- `fragment.go:24-27`：用 `errors.As` 取 `Message` 决定用户看到哪句话；深层协议错误会取代外层提示。

落地规则：`protocol.Error` 只放在最外层，cause 放内层；**逐点迁移，禁止全局 `%w` 替换**；每点配一个反例。

**(3) G29（抽 `serveFragment`） × 非片段端点的状态码契约**

`fragmentIssue` 的契约是「htmx 片段端点一律 200 + 可读 HTML」，而 `/ui/file-text`、`/ui/file-image`、lazy 加载、`/ui/exports/*` **必须保留真实状态码**——它们的调用方是 `response.ok` 或浏览器导航。把助手做成通用写响应函数、顺手用到这些端点上，会让 4xx 变成 200，前端 `if (!response.ok)` 静默失效。约束：助手只在片段渲染路径内使用。

### 6.2 必须合并成一批的

| 合并项 | 为什么不能分开 |
|---|---|
| G03 + G29 | 同一函数（`serveUI` 381 行）；拆路由表与抽助手是同一次重排 |
| G02 + G07 + G12 + G24 | 都在改 transport/presentation 的函数签名，分开做要改两遍 |
| G08 + G24 + G09 | 同一条渲染边界；先 G24 换签名、再决定 G09 要不要接口，反过来接口要写两遍 |
| G04 + G05 + G06 | 同一条错误/回执契约；G06 换哨兵时若让「未启用」被当成失败，没配 storage 的部署会开始拒绝命令 |
| G21 + G27（metadata 那两处） | `lazy.go:55`、`search.go:213`、`metadata.go:58/65` 是同一个问题（拿失败当类型判断），一个 helper 全覆盖，顺带消掉最深的 7 层嵌套 |

### 6.3 顺序敏感

- **G20 必须早于 G22/G23，并且改完要重新采样**：它会消掉首页 139KB/op 里的 ~31KB（22%），profile 的占比会整体移动；在旧数据上继续优化等于白测。
- **G20 的可行性取决于扫描期能拿到什么**。`node` 目前只存 `parent/offset/size/lastModelID`（`store.go:260-265`），而 `lastModelID` 正是扫描期顺手存下来的派生信息（`scan.go:82-87`、`:116-121`，依据 `head.Type == "model_change"`）——**先例已经存在**。但 `isUser` 需要 `message.role`，而快路径刻意只读到 parentId 为止。好在该路径**已经整行扫过一遍**（`balancedJSON(b)` 的 B13 结构校验），所以增量是「顺手解出 `message.role`」而不是「整行重新解析」。
- **G20 不会丢掉「读期间文件变化」的检测**：页循环（`store.go:341-352`）对每条选中的条目都会重新 `ReadAt` + `json.Valid`，而 `alignToTurn` 追加的条目也在同一批里；另外 `scanFile` 遇到坏记录本来就是直接失败。差别只在失败形态：原来是「页更短」，之后是 `conflict` 错误——这是有意选择，要写进改动说明。
- **G18 要排在 G13/G14/G15/G30 之后**，否则 CI 立刻变红；或在接入时先跑基线、用忽略清单过渡。
- **G17（386 个测试改名）单独提交**：已核实 CI 与 `scripts/` 没有 `-run` 过滤，文档也没有引用测试名（只有本文件举的一个例子），所以改名本身安全；但混在行为/性能改动里会让 diff 失去可读性。

### 6.4 常量与接口的连带约束

| 约束 | 细节 |
|---|---|
| `wsReadLimit` | `server.go:68` 与 `pi.MaxImages × MaxImageDataLen`、附件预算、U05 的修复绑在一起，`server_test.go:1272-1279` 有断言。G26 若调整，必须同步测试与前端预检 |
| `MetricsSink` | `runtime/manager.go:102`，目前**只有 `observe.Metrics` 一个实现、没有测试替身**，所以 G05 加一个计数方法成本很低；但它属于 `runtime`，不要顺势扩成通用观测总线 |
| 方法表 | `transport.SupportedMethods`（`server.go:41`）与 `protocol.specs`（`methods.go:59`）由 `methods_test.go:28/47`、`ui_contract_test.go:17/75` 交叉核对；G01 拆分若顺手动方法表，会同时触发这两组测试 |
| `IsUrgent` | `claims.go:224` 用它在派发**之外**决定排队优先级；拆 `dispatchCommon` 时不要把它一起搬进去 |

### 6.5 G28 的跨仓陷阱，以及零跨仓的方案

模板里只有一处 `printf`（`pi-webui-htmx/src/templates/models.html:2`，`printf "%s/%s"`，两个参数都是字符串），而 `%/` **在全部模板里没有任何使用**。

- **推荐**：删掉自定义覆盖，直接用内置 `printf`。`%s/%s` 行为一致；而且内置遇到类型不符会打印 `%!s(int=7)` 这种显眼标记，不再静默出空串。零跨仓改动。
- 若要改名（`key`/`joinSlash`）：模板里出现未知函数会让 `LoadFromDir` 直接失败、桥**拒绝启动**，所以必须「桥先加新名 → UI 改模板 → 桥再删旧名」三步走，不能一次改完。

### 6.6 G24 的两个落点

现状是：`transport` 用 `toAnyMaps` 把类型化切片 JSON 往返成 `[]map[string]any`（`server.go:423/444/483`），`presentation` 再用 `stringField(m,"name")` 按键取值拼回类型化行。吃 map 的入口共 5 个：`RenderFiles`、`RenderDirs`、`RenderPackages`、`RenderSearch`、`RenderGitStatus`。

- **方案 A（改成类型化入参，推荐）**：模板只用**字段名**，已逐字核对——`files.html` 用 `Name/Path/IsDir/Size`，`packages.html` 用 `Name/Source/Version/Latest/HasUpdate/Disabled/Error`，`search.html` 用 `SessionID/EntryID/Title/Cwd/Snippet`——所以模板不用动。代价是要同步改直接构造 map 的测试（`presentation/dirs_test.go:12-54`、`fragments_test.go:34-81`），并保留 `formatSize(intField(e,"size"))`（`presentation.go:744`）的格式化语义。
- **方案 B（就地构造 map，不经过 JSON）**：测试不动、风险最小，省掉 marshal/unmarshal，但每行仍有一次 map 分配。

另：`stringField`/`boolField`/`recordOf` **不能连带删除**——模型面板（`presentation.go:673`）、统计（`stats.go:133`）、分支树（`presentation.go:1036`）都还在用。

### 6.7 明确「不要一起做」的清单

- 不要把 G23（缓冲复用）用到 `pi/client.go`。
- 不要把 G29 的助手用到非片段端点。
- 不要在 G04 迁移里做全局 `%w` 替换。
- 不要把 G01（分发拆分）与 G03/G29（`serveUI` 重排）放进同一次提交：都是 `server.go` 的大块移动。
- 不要把 G17（测试改名）与任何行为/性能改动放同一次提交。
- 不要为 G27（嵌套深度）单独重构：真正值得动的两处已经在 G21/G28 里。
- G19（magic-context 的 SQL 构造）与本轮所有改动解耦，保持不动。

## 7. 明确不算问题（避免后续误改）

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

## 8. 建议的批次与顺序

批次按上面的耦合结论排（每批一个提交；标注「独立」的可以随时插队）：

| 批次 | 内容 | 前置 | 关键约束 |
|---|---|---|---|
| **A 清理** | G13、G14、G15、G16、G30 + G17 的 `t.Setenv`（不含改名） | 无 | 零行为变化；G15 删除不可达分支时要留注释说明 `len==1` 只可能因为超字节上限 |
| **B 静态检查** | G18 接入 staticcheck | A | 先跑基线再定规则，别一开始就 `-fail` |
| **C 错误与回执契约** | G04（只加能力）+ G05 + G06 | 先补三个反例：204 分支、`outcome_unknown`、fragment 提示文案 | 逐点迁移；`protocol.Error` 留最外层；「未启用」不等于失败 |
| **D 内容形状判定** | G21（`lazy.go` / `search.go` / `metadata.go` 三处同一 helper） | 无 | 顺带消掉 G27 的两处最深嵌套 |
| **E 渲染边界** | G24（方案 A）+ G08 | 已核对模板字段名（§6.6） | 勿连带删除 `stringField`/`recordOf`；同步改 `dirs_test.go`/`fragments_test.go` |
| **F 扫描期 kind** | G20 | 无 | 扩展 head 解析（不是整行重解析）；**改完立刻重测 pprof**，再决定 G22/G23 |
| **G 内存收尾** | G22（一个提交）→ G23（另一个提交，范围受限） | F | G23 **排除 `pi/client.go`**；两处都涉及切片所有权，各自配 `-race` 与基准对比 |
| **H printf** | G28 零跨仓方案（删覆盖） | 无 | 若改名则必须三步跨仓 |
| **I HTTP 签名** | G02 + G07 + G12（`transport.Config`、`encoding` 具名类型、nil 语义统一） | C、E | G12 要给 `New` 加 `ui != nil` 守卫（现在靠 `SetMagicContext` 的 nil 接收者兜着，调用点在 `server.go:126`） |
| **J serveUI** | G03 + G29 | I | 助手只用于片段端点 |
| **K 分发拆分** | G01 | J | 保持 `claims`/`IsUrgent` 在派发之外；方法表保持单一来源 |
| **L 配额通知** | G10 | 独立 | 先跑 `replay_ws_test.go` 联测与 `replay_queue_test.go` 的四个补发反例 |
| **M 测试改名** | G17 剩余部分 | 独立 | 纯机械提交，不夹带行为改动 |
| **N 记录不动** | G19、G26 | — | G26 只补文档说明内存上界 |

每步的验收沿用仓库既有门槛：`test -z "$(gofmt -l .)"`、`go vet ./...`、`go test -race -count=1 ./...`，跨仓改动再跑 `scripts/verify-pair.sh`。

两点提醒：

1. A/B/D/H 四批都不改行为，可以连续做掉；C/F 是唯一需要重新采样的两批。
2. 别名类缺陷（G23 若越界）与错误码改道（G04 若迁移过头）都不会被 `-race` 或现有断言发现，只能靠**先写反例**——这也是本仓库既有的做法（先红后绿）。

## 9. 与既有台账的关系

`docs/code-audit.md` 维护 B01–B81 与 U01–U21 两组编号，内容偏**正确性与安全**；本文件用独立前缀 G，记录的是**语言惯用性与结构**，两者可能落在同一段代码上但视角不同，不重复立号。

落修前应先查 `code-audit.md`：若某个 G 项与既有 B/U 项指向同一处，应合并到那一条修复里一次改完，避免同一段代码被两次重构。本文件未做这项比对。

## 10. 落地记录

按第 8 节的批次推进；每个批次一个提交，逐步追加。

| 批次 | 提交 | 内容 | 与计划的差异 |
|---|---|---|---|
| A | `b59f747` | G13/G14/G15/G16/G30 + `t.Setenv` | 无 |
| B | `7103f6a` | G18：接入 staticcheck v0.8.1、清理基线 | 见下 |
| C | `eb70488` | G04 能力 + G05 + G06 | 见下 |
| D | `6dd4a38` | G21：三处「拿失败当形状判断」统一为首字节判定 | 无 |
| E | `8bac2de` | G24 + G08：五个渲染入口改收具名行，`git.status` 也类型化 | 无 |
| F | `b32f587` | G20：扫描期顺手记 user 锚点，轮边界对齐不再读盘/反序列化 | 契约收紧见下 |
| G1 | `5cb5f65` | G22：整页一次分配，`selected`/`Entries` 预分配 | 无 |
| G2 | `025e94d` | G23：`jsonl.Reusable` 复用缓冲，三个安全调用点切换 | `lazy.go` 经核对不安全，未切 |
| H | `acf5ad0` | G28：删除对内置 `printf` 的覆盖 | 无 |
| I | `a00615a` | G02 `transport.Options` + G07 具名状态与具名布尔 + G12 nil 约定统一 | 两个协议布尔保留原名 |
| J | `fa6bc38` | G03 + G29：`Encoding` 具名类型、serveUI 按契约拆三个函数、11 处样板改 `renderFragment` | 顺带修 history 400 → 200 |
| K1 | `504c1b3` | G01 结构：`dispatchCommon` 按域拆成 8 个 `dispatch*`（`dispatch.go`），60 个 case 纯搬移 | 无 |
| K2 | `57bdf47` | G01 类型：36 处回执改具名类型（`responses.go` / `runtime/replies.go` / `management.ModelsReply`） | 用户文档与诊断 map 不动 |
| L | `5b21aae` | G10：出站字节配额由 2ms 轮询改为归还时广播 | 见下（实测数据） |
| N1 | `dbbb207` | G25：bash 输出缓冲按文件大小分配 | 无 |
| N2 | 本次 | G17 决定不改名（见下）+ G19/G26 登记 + M/N 收尾 | 见下 |

### 批次 A

按计划执行。补充两点观察：`extensionState.forget` 删除时发现它的注释描述的语义（按会话清空前缀状态）根本做不到——状态 key 由插件提供、不按会话隔离，所以顺手把 `update` 的文档改成描述真实生命周期；`presentation.Now` 删除后该包不再有可变全局变量。

### 批次 B

**版本是硬约束**：staticcheck 2025.1.1 及更早读不了 Go 1.27 的导出数据，会先报 `export data version 4 is greater than maximum supported version 2` 再拒绝检查。CI 固定到 v0.8.1。

基线共 16 条，其中 4 条是 ST1005（错误字符串不应大写开头）——中文提示以 `Pi`/`Git` 开头会被这条规则误报，属规则与项目约定的冲突，用 `staticcheck.conf` 排除并写明理由；其余 12 条都是真问题：4 个死符号（`maxContentBytes`、`isEOF`、`extensionState.forget`、两个未使用的测试辅助函数）、2 处「赋值后从不读取」（其中 `Store.alignToTurn` 一直在返回一个没有调用方使用的字节预算）、以及 `leafID`/`handleUIResponse` 两处首字母缩写不一致。

### 批次 C

**G04**：`protocol.Error` 增加未导出 `cause` 与 `Unwrap`，新增 `Wrap(code, message, cause)`；本次只迁移三处（`public_origin` 解析改 `%w`、`discovery` 出站与读取、`config` 序列化），其余调用点保持原样，避免一次性改动面过大。

三条回归先写后改：`Wrap` 可回溯原因且外层码优先、错误对象序列化后只有 `code`/`message`（不泄露原因）、片段提示只渲染外层 `message`。另加一条针对 204 分支的守卫：**损坏的历史文件是 `invalid_history`，不得被当成「分支尚未落盘」**——那会把数据损坏显示成一个稍后会出现的空分支。

**G05/G06**：`Record` 现在把写盘/轮转失败向上返回（内存索引照旧更新，进程内去重不受影响），「未启用」改用 `storage.ErrNotEnabled` 而不是借用 `os.ErrClosed`。传输层新增 `storeReceipt` 统一落盘并统计失败：`ErrNotEnabled` 是配置事实、不计入失败；真实写失败计入 `receiptFailures` 指标，`/healthz` 与 `receipts.degraded` 都能看到。

**一个仍需拍板的取舍**：intent 写失败目前仍然继续派发（fail-open），只计数与暴露降级。严格的做法是 fail-closed——写不进 intent 就拒绝有副作用的命令——否则「结果未知不重发」的承诺在重启后没有依据。改成 fail-closed 会改变对外行为（存储不可用时全部写命令被拒），因此留给明确决策，不在本批单方面改。

### 批次 D

三个调用点（`lazy.go`、`search.go` 的 `flattenContent`、`metadata.go` 的标题回落）原本都靠「试一次、失败再试另一种」判断 `content` 是字符串还是块数组。现在统一用 `shapeOf` 看首个非空白字节。

实测（`BenchmarkRenderHistory`，各 3000 次采样）：

| 指标 | 改前 | 改后 |
|---|---|---|
| 时间 | 323 µs | 276 µs（−14.5%） |
| 分配字节 | 154310 B/op | 142710 B/op（−7.5%） |
| 分配次数 | 1664 allocs/op | 1511 allocs/op（−9.2%） |
| 错误对象构造（`transformUnmarshalError` + `newUnmarshalErrorAfter`） | 约 9KB/op | 已从 profile 中消失 |

同一个提交顺带消掉了第 5.2 节记录的两处最深嵌套（`metadata.go` 由 7 层降到 3 层）。

新增回归：形状判定本身（含前导空白、`null`、空值）、`flattenContent` 与 `scanLazyBlocks` 在字符串/数组/null/对象四种输入下的行为。

### 批次 E

五个渲染入口（`RenderFiles`/`RenderDirs`/`RenderPackages`/`RenderSearch`/`RenderGitStatus`）改收具名行类型，`transport` 侧用四个显式转换（`fileRows`/`packageRows`/`searchHits`/`gitStatus`）替代 `toAnyMaps` 的 JSON 往返；`workspace.GitStatus` 也从 `map[string]any` 变成结构体（JSON 标签保持一致，`git.status` 对外响应不变，连 `from` 键都留着）。

实测同一个 500 项转换（临时基准，测完即删）：

| 指标 | `toAnyMaps`（改前） | `fileRows`（改后） |
|---|---|---|
| 时间 | 589,745 ns/op | **4,912 ns/op** |
| 分配字节 | 359,921 B/op | **24,576 B/op** |
| 分配次数 | 7,017 allocs/op | **1 alloc/op** |

顺带被 staticcheck 指出的两个死函数（`boolField`、`intField`）随废弃的 map 路径一起删除；`stringField`/`recordOf`/`anyList` 仍被模型面板、统计与分支树使用，保留。

`events.Ring.Stats()` 保持 `map[string]any`：它只用于测试与诊断，不跨渲染边界，类型化收益有限。

### 批次 F

扫描阶段本来已经读了每行的行首（`type`/`id`/`parentId`），现在顺手把「这条是不是 user 消息」记进 `node`，`alignToTurn` 直接查表。`readEntry` 与 `entryKind` 随之删除。

`IsUser` 的取法：走到 `message` 成员时不读它的值，只降一级看它的第一个成员是不是 `role`（真机 3964/3964 条 `role` 都是 `message` 的首成员）。大行因此不付代价——`BenchmarkEntryHeadIsUser` 里 112 KB 行 354 ns / 32 B/op，与小行（330 ns / 24 B/op）几乎无差。**刻意不用「在整行里搜 `"role":"user"`」**：正文里出现这段文本就会判错，回归用例 `Test正文里的role文本不影响判定` 专门钉住这一点。

**契约收紧**：`role` 不是 `message` 首成员、`message` 不是对象、`message` 成员缺失，这三类现在放弃快路径、交慢路径裁决（结果仍正确，只是慢）。慢路径的单次解析里顺带取出 `role`（`Message struct{ Role string }`），因此它对 iso-late 的顺序也一致。

实测（`BenchmarkHistory首页/100轮/0.7MB`）：

| 指标 | 改前 | 改后 |
|---|---|---|
| 时间 | 299 µs | **179 µs**（−40%） |
| 分配字节 | 138087 B/op | **106461 B/op**（−23%） |
| 分配次数 | 124 allocs/op | **83 allocs/op** |

**行为等价性验证**：把一份真机 63 MB 会话（17042 条）冻成副本，用「改动前的 HEAD 工作树」与「改动后」各跑一遍逐页翻到开头，**41 页 / 6195 条的首末条目 ID 与 HasMore 完全一致**；同时对全部条目比对 `node.isUser` 与完整投影的 `KindUser`，17042 条零不一致。这两项验证是一次性的（真机会话不进仓库），脚本已删除。

**缓存估算**：`node` 变大一个 bool；`scanCache.put` 的估算本来就是 `len(nodes) * 64`（注释写明实际约 40 字节），加上 bool 后仍在同一量级，因此没有改估算公式，只在该注释里点明了这一点。

### 批次 G1（G22）

整页一次分配：条目长度之和就是所需字节数，各条目成为这块缓冲的子切片；`selected` 按 `limit` 预分配，`Entries` 按 `len(selected)` 预分配。长度在 `alignToTurn` 之后重算，因为它会追加条目且不再回传累加值。

| 基准 | 改前 | 改后 |
|---|---|---|
| 首页分配次数 | 83 | **23** |
| 首页分配字节 | 106461 | 103761 |
| 连续翻页（10 页）分配次数 | 1036 | **370** |
| 连续翻页分配字节 | 1225588 | 1191349 |

新增回归 `Test页条目不被后续读取改变`：页条目在自己被丢弃前内容不因后续 `History` 调用而改变——把「不要把这块缓冲池化」这条边界钉住。

### 批次 G2（G23）

冷扫描的分配剖析显示 `jsonl.Read` 占 **79% 的分配字节**（14MB 会话 ×20 次 = 600MB 中占 472MB）：每条记录都 append 到 nil 切片，超过 bufio 默认缓冲（4KB）的长行按倍增反复扩容拷贝。

新增 `jsonl.Reusable`（复用内部缓冲的读取器）。**做成类型而不是多加一个参数**：约束（返回的字节只在下一次 `Read` 前有效）应当写在名字上。

调用点的安全核对结果与计划有出入：

| 调用点 | 判定 | 处置 |
|---|---|---|
| `sessions/scan.go` | 只存 offset/size | 已切 |
| `sessions/search.go` | 只产出 string | 已切 |
| `sessions/metadata.go` | 只产出 string | 已切 |
| `sessions/lazy.go` | **把 `message` 子切片直接返回给调用方** | **未切**（计划里标「待审」，审下来是不安全） |
| `sessions/index.go` | 每次调用只读一行 | 不改（复用一个缓冲没有收益） |
| `pi/client.go` | 响应与扩展对话框保留字节 | 未切 |

| 指标 | 改前 | 改后 |
|---|---|---|
| 冷扫描分配字节 | 28177828 | **3806093**（−86%） |
| 冷扫描时间 | 47.7 ms | 42.2 ms |
| 冷扫描分配次数 | 66601 | 56348 |

分配次数下降有限是预期的：剩下的来自 node map、`unquoteJSON` 的字符串与 JSON 错误对象，不在这条路径上。

新增回归 `TestSearch多条命中各自摘要不串行`：30 条内容互不相同的命中必须各自报自己的摘要、无重复，且同一查询连跑两次结果一致——缓冲区误用的表现正是「摘要串到相邻记录」。

### 批次 H（G28）

删除 `funcMap` 里对内置 `printf` 的覆盖。被删实现只认 `%s` 与 `%/`：`%d` 原样输出、非字符串的 `%s` 静默变空，写错不报错只显示错。模板实际只用到 `printf "%s/%s" .Provider .ID`（两个字符串），内置语义完全覆盖。

两条回归：`Test模板printf用内置语义` 断言 `funcMap()` 不再定义 `printf` 且内置语义可用；`Test模型选择器selected仍正确` 断言被删函数唯一的真实用途（模型下拉的 `selected` 判定）不受影响。

### 批次 I（G02 + G07 + G12）

**G02**：`transport.New` 从 13 个位置参数改为 `transport.Options` + `Validate()`，返回 `(*Server, error)`。取值约束从 `cmd/pi-bridge/main.go`（那里有一份长度检查，测试路径完全没有）收进构造处，错误信息点名字段。`Validate` 对 `Token < 32` 的提示直接写成「检查是否与 Host 写反」——这正是该签名最现实的错法。

回归 `Test构造参数校验` 覆盖 6 种错配（token 太短、token 位置填成监听地址、host 为空、缺 manager/store/files）；`TestUI层可选但缺了要有明确表现` 与 `TestUI缺席时片段路径不panic` 覆盖可选字段。

**G07**：
- `runtime.workerStatus` 具名类型 + 7 个常量（`starting`/`running`/`idle`/`waiting_input`/`stopping`/`stopped`/`failed`），JSON 标签不变（协议字段），9 处字面量赋值改常量。
- `Worker.stop(force, idleOnly bool)` 增加两个具名入口：`Stop(force)`（协议字段，保留该名）与 `stopIfIdle()`（回收器调用）；`stop` 降为共同实现并注明从具名入口调用。
- `Terminal.Close(force bool)` 拆成 `Close()`（优雅）与 `ForceClose()`（宽限期后 SIGKILL 进程组）。调用点按真实语义改名——`Close(true)` 只是读不出意图，而强制杀掉进程组会让组内其它进程没有收尾机会，所以这不是纯改名。
- 两个相邻布尔返回值加具名结果：`admit` → `(accepted, urgent bool)`、`SubscribeWithReplay` → `(sub, info, replay, replayed, err)`。
- **未改**：`Bash(…, excludeFromContext bool)` 与 `Worker.Stop(force bool)`。两者的布尔直接对应协议字段（`bash.run` 的 `excludeFromContext`、`session.stop` 的 `force`），调用点就是把解码出的同名字段传进去；为它们造包装类型只会让协议字段与 Go 标识符之间多一层映射。

**G12**：约定统一为「Renderer 非 nil，禁用 UI 由外层决定」。删除 `SetMagicContext` 的 nil 接收者容错（它曾是全仓唯一一处 nil 安全方法，等于把「记得判空」的责任推给每个人），`transport.New` 改为条件注入；`fragmentIssue` 补上自己的守卫，使将来新增的片段调用点不会踩到 panic。

### 批次 J（G03 + G29）

**`Encoding` 具名类型**：`EncNone`/`EncBrotli`/`EncGzip`，`PickEncoding` 返回它，压缩、资产缓存与 HTTP 写回三层都改。协商结果仍然**显式传参**而不是塞进 context：它决定响应头，藏起来只会让头的判断更难跟。

**serveUI 按契约拆成三个函数**：`serveUI`（分发）→ `serveUIAssets`（`/`、`/assets/`，真实状态码 + 缓存语义）/ `serveUIFragments`（`/ui/*`，一律 200 + 可读 HTML）。行数 381 → 27 / 39 / 271，两个相反的错误约定再也不会被写进同一个 switch。

**`renderFragment`** 取代 11 处「渲染→报错→写回→return true」样板；它把取数与渲染收进一个闭包，两个出口都固定为 200，调用点做不到「只做一半」（例如渲染失败回状态码）。文档注明**只给 htmx 交换的片段端点用**。

**顺带修掉一个真缺陷**：`/ui/sessions/{id}/history` 对「会话已不存在」回 400 —— 这是状态类失败，htmx 不交换，界面于是停在旧会话的历史上、没有任何提示。改为按片段约定渲染说明；模板缺失这类程序性故障仍回 500。浏览器实测：`#turns` 里出现「会话不存在」。

**新增三层回归**（`status_contract_test.go`）：15 个片段端点的状态类失败必须 200 + HTML；非片段端点（资源 404、越界路径 400、非法会话 ID 400）保留真实状态码；扩展端点的 204/400 信号不动；外壳能渲染且 br 协商仍生效（具名类型最容易坏在这里）。

### 批次 K（G01，结构部分）

`dispatchCommon` 696 行 / 60 个 case → 131 行的路由 + `dispatch.go` 里 8 个域函数：

| 域 | case 数 | 需要什么 |
|---|---|---|
| `dispatchRun` | 10 | ctx + worker（发送、排队、思考/压缩开关、中止与停止） |
| `dispatchSession` | 18 | ctx + worker（状态读取与身份变更） |
| `dispatchBash` | 3 | ctx + worker |
| `dispatchDialogs` | 2 | ctx + worker |
| `dispatchSessionOps` | 3 | ctx（磁盘会话：搜索/删除/导出） |
| `dispatchConfig` | 9 | ctx |
| `dispatchWorkspace` | 8 | ctx（files.* / git.*） |
| `dispatchTerminal` | 5 | 连接（终端输出要回到发起它的连接） |

**「未处理」用哨兵错误表达，而不是第三个返回值。** 理由是可验证性：域函数内那几十处 `return` 因此**一字未改**，这批拆分是纯搬移。搬移后我把 HEAD 里 60 个 case 的正文与新树逐条对比（去空行、`empty()`→`decodeEmpty(r.Params)` 归一），**差异 0 处**；method 集合也完全一致（67 个 `case`，无丢失无新增）。

顺序有意保留：`worker.list` 与 `session.start` 仍在取 worker 之前处理——`start` 的任务正是创建那个进程。

**事实来源仍然有三处**，这次没有增加也没有减少：`SupportedMethods`（能力清单）、`protocol.specs`（执行策略）、各域 switch（实际分发）。前两者由 `methods_test.go` 静态核对；第三者由 `Test能力清单与实际分发一致` 逐方法实际调用兜住（声明支持却未实现会红）。新增方法三处都要改，这一点写进了 `dispatch.go` 的包注释。

### 批次 K2（G01 的类型部分）

36 处回执从 `map[string]any{…}` 改成具名类型：`transport/responses.go` 28 个、`runtime/replies.go` 5 个、`management.ModelsReply` 1 个。**同一形状共用一个类型**（`session.steer` 与 `session.follow_up`、`config.models.discover` 与 `config.catalog` 等），想改必须一次改到全部。

**两处顺带修掉的真问题**：

1. `sessions.delete` 有两条回执路径：普通路径返回类型化的 `DeleteResult`，「连带停掉运行中会话」那条**手抄了一遍** `sessionId`/`trashed`/`path` 三个键。`DeleteResult` 一旦加字段，两条路径就会给出不同形状。现在是一个类型 + `omitempty` 标记。
2. `session.set_model` / `session.cycle_model` / `session.fork` 在 `runtime` 里手拼模型对象与三个字段，同样是「键名只在字面量里」的问题。

**明确不动的两类**（规则写进了文件头注释）：用户文档透传（`models.json` / `settings.json` / `trust.json` 的内容）与诊断快照（`/healthz` 的 stats）。前者的形状由用户决定，后者的采集面本身开放——类型化等于假装它们有固定模式。

**验证**：把 36 个方法的键集合与改造前的 map 字面量逐一对照（脚本从旧源码解析键名、与新类型的 json 标签比对），**零差异**；新增 `responses_test.go` 把每个形状的键集合钉在产生它的方法旁边，`protocol.md` 收同一张表（有意重复：测试的职责是独立重述期望）。

桥自建的**事件载荷**（`terminal_closed`、`terminal.output`）也一并类型化，因此分发路径上不再有任何 map 字面量；**Pi 自己的事件保持原样透传**，形状属于 Pi。

### 批次 L（G10）

`enqueueBounded` 以前用 CAS 抢占 + `time.After(2ms)` 轮询等待字节配额，归还的一侧不发通知。改成 `outboundQueue`：

- 检查条件与挂载等待**在同一把锁下**（消除漏唤醒）；
- 归还时若有人在等，`close` 掉当前代次的信号通道并换新——**广播**，且只在真有竞争时才分配，平稳路径零分配；
- 单播式唤醒（只叫醒一个）会让其余等待者白等到下一次归还，所以这里是广播而不是单个令牌。

**实测**（同一台机器，两个工作树各跑同一段测量代码）：测「归还瞬间 → 等待者拿到预算」的延迟，20 轮取平均。

| 实现 | 平均唤醒延迟 |
|---|---|
| 旧（2ms 轮询） | **1.074 ms** |
| 新（归还时通知） | **10 µs** |

旧实现的 1.074ms 与「[0, 2ms] 上均匀分布的期望 1ms」一致——这项交叉验证说明测量方法本身没有偏。

**一个失败的测量也记下来**：先试图用 `voluntary_ctxt_switches` 数唤醒次数，结果新旧都是 0 或 1。原因是 Go 的定时器唤醒走 netpoller，多数情况下不产生 OS 线程切换，这个计数器抓不到轮询。它证明不了任何事，已放弃。

**回归**（`send_queue_test.go`）：一次归还唤醒全部等待者（单播实现会失败）、无人等待时归还的空间不丢、等待被取消后不残留唤醒计数、超单帧上限与空帧直接拒绝。

### 批次 N1（G25）

`ReadBashOutput` 先 `make([]byte, maxBytes)`（上限 8 MiB）再看文件多大。改成按**实际会读到的字节数**分配。回归用 `runtime.MemStats` 观测分配量而不是断言具体数字：读 9 字节的文件不得分配接近上限的量。**反向验证**——把分配改回按上限，测试如期失败并报出「小文件读取分配了 8394784 字节」；改回后通过。另一条测试钉住截断路径仍只取尾部。

### 批次 N2：决策与登记

**G17（测试改名）——本轮决定不改，理由用实测数字说话。**

现状：454 个测试函数中 423 个名称含中文，其中 **246 个在 `Test` 之后直接跟中文**，其余 177 个有 ASCII 前缀。

| 判断项 | 实测 |
|---|---|
| `go test -run 'Test补发缺口明确失败'` | **可用**（中文参数正常匹配） |
| `go test -list '.*'` | 可用 |
| CI / `scripts/` 是否有 `-run` 过滤 | **没有** |
| 文档是否引用测试名 | 只有本文件（改名需同步 6 处） |
| 改名规模 | 79 个文件、423 个函数 |

改名没有任何行为收益，却要在 79 个文件上产生数百行纯搬运的 diff，并让这些文件的 `git blame` 失去意义。工具链层面唯一的代价（`-run` 要输入中文）实测不成立：中文参数可用，且 177 个有 ASCII 前缀的用例本来就能用前缀选取。

因此**不做**；该条目里唯一有实质收益的部分（`os.Setenv` → `t.Setenv`）已在批次 A 完成，全仓现已无 `os.Setenv`。

这条与本文件其它条目不同：它是**主动决定不做**，不是遗漏。代价与影响已量化，任何时候都可以单独执行。

**G19（magic-context 的 SQL 构造）——登记，不改。** `internal/magiccontext` 走 `sqlite3` 命令行、参数校验后代回 SQL 字符串，与 `database/sql` 的惯用做法相反，但这是「不引入 CGO/驱动」的显式取舍，且已配套沙箱与白名单校验。

**G26（WS 读上限的乘数效应）——登记，不改。** `wsReadLimit` 决定单连接最坏的读取缓冲（约 97 MiB × 8 连接）。它与 `pi.MaxImages × MaxImageDataLen`、附件预算、U05 的修复以及 `server_test.go` 的断言绑在一起，调整必须同步前端预检，属于**跨仓联动改动**，不该混在本次清理里顺手做。
