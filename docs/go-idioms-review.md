# Go 惯用写法复核

复核日期：2026-09-30。基线：桥 `863041d`（工作树干净）。**本文是源码复核记录，不是修复声明；标出的问题都还没改。**

对象：`internal/` 下 18 个包与 `cmd/`，约 16,700 行非测试代码（另有 83 个测试文件、13,571 行）。`go.mod` 为 go 1.27.1，直接依赖 3 个（brotli、coder/websocket、creack/pty）。

结论：代码在**并发安全与安全边界**上明显高于平均水平（无 `panic`/`goto`/`reflect`/`ioutil`，安全路径全部走 `crypto/rand` 与 `subtle.ConstantTimeCompare`，`context` 一律是首参与不入结构体，`http.Server` 有完整超时，测试没有在 goroutine 里调 `t.Fatal`）。不那么 Go 的地方集中在三块：**错误链几乎不存在**、**类型表达靠字符串与匿名容器**、**少数函数与构造器体量失控**。

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

没做的：本机未安装 staticcheck/golangci-lint，因此下文 G18 只给建议未给基线；未做性能基准；未逐条比对 B/U 台账（落修前应先查重，见第 7 节）。

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

## 5. 明确不算问题（避免后续误改）

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

## 6. 建议的修复顺序

按「风险低 → 风险高」排，前两步可以立刻做且几乎无回归风险：

1. **零风险清理**（G13、G14、G15、G16、G17 的 `t.Setenv`）：删假引用与死代码、补包注释。一个提交。
2. **接入 staticcheck**（G18）：先看基线，再定规则。它可能再报出本文件没列到的同类问题。
3. **错误链**（G04）：`Error` 加 cause 与 `Unwrap`、新增 `Wrap`；先只加能力不改调用点，再逐包迁移。需要回归的错误路径：`public_origin` 解析、`discovery` 出站、`magiccontext` 存储不可用。
4. **回执可观测性**（G05、G06）：加降级计数/健康位，`Record` 语义收敛。
5. **类型表达**（G07、G08）：`status`/`encoding` 具名类型、`stop(force, idleOnly)` 拆分、渲染边界结构体。面较大，建议按包分批。
6. **结构性重构**（G01、G02、G03、G09、G12）：分发拆分、`transport.Config`、`serveUI` 拆表、nil 语义统一。这批动的是主干，建议单独排期，并在开始前先补齐「62 个方法逐一调用」的契约回归——现有 `ui_contract_test.go` 与 `methods_test.go` 是基础。
7. **G10（并发配额通知）**：与 G01 同批或独立，改动前先跑补发环联测与 `-race`。

每步的验收沿用仓库既有门槛：`test -z "$(gofmt -l .)"`、`go vet ./...`、`go test -race -count=1 ./...`，跨仓改动再跑 `scripts/verify-pair.sh`。

## 7. 与既有台账的关系

`docs/code-audit.md` 维护 B01–B81 与 U01–U21 两组编号，内容偏**正确性与安全**；本文件用独立前缀 G，记录的是**语言惯用性与结构**，两者可能落在同一段代码上但视角不同，不重复立号。

落修前应先查 `code-audit.md`：若某个 G 项与既有 B/U 项指向同一处，应合并到那一条修复里一次改完，避免同一段代码被两次重构。本文件未做这项比对。
