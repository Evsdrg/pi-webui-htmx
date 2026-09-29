# 前端改动复核：htmx 职责、TypeScript 与 CSS

日期：2026-09-30；第1–5节保留审查基线 UI `41e3ee7` 的原始证据（当时未修改产品实现）。桥优化研究已先提交 `a82ae7e`。**当前落地结果见第6节**，跨仓方案见 [frontend-repair-plan.md](frontend-repair-plan.md)。

## 1. 判据与结论

**整体是服务端 HTML 片段 + 浏览器增强，但模型编辑器、目录/记忆导航和异步归属仍未达到本项目约定。** htmx 不禁止 JS；把所有 fetch 改成 hx-get 也不是验收标准。

- 服务端已有权威数据的列表、参数表、分页动作，优先由 Go 模板生成 HTML 与下一步动作。
- 未提交草稿、WS 增量、剪贴板、拖动、主题偏好、富内容增强属于浏览器。草稿管理不能为了“少写 TS”而丢失或泄露秘密。
- 控件声明请求与目标；确需 JS 编排时，明确作用域、请求代次与释放时机。
- CSS 必须核最终级联与实际布局，不能把“写了 transition”当作有动画，也不能只用选择器文本契约证明视觉正确。

官方依据：htmx [hx-sync](https://htmx.org/attributes/hx-sync/)、[events](https://htmx.org/events/)、[Locality of Behaviour](https://htmx.org/essays/locality-of-behaviour/)。用 find-docs 查询官方仓库资料，并核本地 `htmx.org/dist/htmx.esm.js` 2.0.11：请求互斥默认按发起元素；`beforeOnLoad` 先于响应头处理，`beforeSwap` 决定是否进入 swap；OOB 在 swap 内处理。因此 **不能笼统说 beforeSwap 拦不住 OOB**，它拦不住的是更早的 HX 响应头副作用。

## 2. 逐项覆盖原来的需求与提交

| 改动 | 审查状态 | htmx/TS/CSS 判定 |
|---|---|---|
| ① 思考/工具块（`9198f94`） | ⚠️ | 原生 details/summary + Go 模板正确；工具预览重复全文（桥 O02）；思考加载仍用 fetch/JSON 拼 div，可改片段；资源释放见 F17 |
| ② 动效（`ed64a62`） | ❌ 部分失效 | 顶栏 hidden 打断过渡；右栏列数变化不宜靠 grid 插值；原生对话框生命周期样式见 F10/F11 |
| ③ 设置与扩展（`5f45e60`） | ✅/⚠️ | 模型已独立；本地主题/字号/宽度留 TS 合理；扩展是只读包清单，不能宣称完整插件/技能管理；首次失败重试见 F18 |
| ④ 模型配置/三态/紧凑样式（`11578cc`、`30f74ac`、`dd547a7`） | ❌ | 表单可用但草稿、保存、重命名、监听生命周期有缺陷；客户端承担树和固定行渲染；见 F01–F06/F16 |
| ⑤ 新会话目录浏览（`cd29e23`） | ❌ 部分 | 列表与 OOB 路径服务端渲染正确；不同按钮在途请求无共同同步域，未约束提交路径与已浏览路径一致；布局被通用规则覆盖 |
| ⑥ 新建按钮显示 cwd（`cd29e23`） | ✅/⚠️ | 内容来自当前会话，TS 更新与左省略合理；实际没有 home→~ 转换（代码明确不猜 home）；不得声称完全相同 |
| ⑦ magic-context（`41e3ee7`） | ❌ 部分 | 只读 HTML 片段正确；完整正文缺入口、下一页替换而非追加、按钮尾页仍出现；导航动作藏在 Workbench 中 |
| ⑧ 编辑并重发（`ed64a62` 等） | ⚠️ | 模板仅 UserText 非空时输出 fork，孤儿 assistant 无 fork；但按钮仍在整轮尾部，不是用户气泡操作栏；分支语义与 Pi Web 原地编辑不同 |
| ⑨ 用量（`ed64a62`） | ✅ | Go 累加/格式化、模板展示，未把数值 HTML 重建放回客户端；速度统计按决定不做 |
| ⑩ 文件树常驻侧栏（`eb33cce`） | ⚠️ | Go 列表 + htmx 交换，右侧文件页已移除；目录点击绕经 TS 触发隐藏字段，拖动公式、旧 CSS 和预览竞态见 F09/F12/F14 |
| 顶栏系统/工具/统计 | ✅/⚠️ | 服务端表格与详情、一份工具快照本地切选合理；慢响应归属缺少保护见 F08 |
| CSS 三表/主题/对比度 | ✅/⚠️ | tokens/app/code 分职仍在，高亮按需加载；新功能又添重复/死规则，旧文档“无死代码”结论失效 |

## 3. 问题台账

证据分级：**探针**=执行当前代码复现；**浏览器**=实际构建 CSS 的隔离 DOM 测量；**源码**=路径可确定，尚未做端到端交互重放。优先级 P1 为数据丢失/错误目标，P2 为功能或布局，P3 为结构与维护。

### F01 / P1 / ❌ 模型配置有多份不同步的草稿（探针）

`src/modules/models.ts:216` selectProvider、`:230` selectModel 直接把 this.doc 写进输入；commitProvider/commitModel 只在 save 时调用。改模型一名称→点模型二→回模型一，新值消失。JSON textarea 只在 reload 中更新，切到 JSON 也不合并当前表单；add/delete 改 doc 后 JSON 仍旧。

`save():454` 末尾无条件 reload，保存等待期间继续输入 JSON 被覆盖。旧桥台账 `docs/code-audit.md` **U19 标“已修”**，当前重做模型编辑器后已回归，应重新打开。

方向：明确一个编辑文档与 revision；节点切换先提交本地字段（不落盘）；JSON 与表单间有明确转换/校验边界；保存发快照，只有 revision 未变化时才接收重读。重读后同时重填选中表单或清空失效 selection。不要按聊天 SessionScope 取消全局配置草稿。

### F02 / P1 / ❌ 供应商改名覆盖同名节点（探针）

`models.ts:320` commitProvider 把旧键映射成 nextName，再 next[nextName]=updated，无重名检查。a/b 两个 provider，把 a 改名 b 保存，payload 只剩一个 b，原 b 的模型丢失。

方向：冲突明确拒绝；前后端同一校验，不用对象覆盖充当迁移。

### F03 / P1 / ❌ 删除末项无法保存（探针）

`models.ts:429` deleteProvider 清空 selection，`:454` save 在没有 selection 时拒绝；删除所有 provider 后界面变化但不能写回。转 JSON 也仍是 reload 时的旧文档。

方向：保存对象是整个编辑文档，不能要求当前必须选择一个节点；空 providers 是明确操作，应支持且保持前后端约束一致。

### F04 / P2 / ❌ 每次选模型重复绑定委托监听（探针）

`models.ts:246` renderThinking 先 replaceChildren，但在固定的 #mm-thinking 上每次 addEventListener('click', 新闭包)。选择5次，固定容器上新增5个 click handler；销毁子元素不会清除父元素监听。

方向：挂载一次的委托 + disposer。固定七行可由模板提供，不在每次切换中重新创建整套控件。

### F05 / P2 / ⚠️ discover/test 的旧响应可覆盖新选择（源码）

`models.ts:494/514` 请求后直接写共用 #discover-result，未捕获 provider 或请求序号，也无取消/重复提交保护。A发现慢、B测试快，A最后回来覆盖B结果。

方向：结果归属 provider+请求序号。若迁 HTTP 片段，用共同 hx-sync replace 域并保留响应归属守卫；不能只把 WS request 换成 hx-get。

### F06 / P2–P3 / ⚠️ 模型编辑器的 HTML 生成边界与可访问性（源码+探针）

549行 models.ts 同时管理协议、文档变换、HTML、SVG、表单、发现结果。`renderTree:101` 与 `renderResult:530` 是服务端已有数据→DOM；`renderThinking:246` 的七行结构是固定 UI。与 docs/components.md 的既有原则不一致。

但编辑过程中树也反映**未提交本地草稿**，不能简单改成每点一下重读磁盘。建议：服务端提供初始树/发现结果片段，固定映射控件放模板；浏览器保留编辑文档、字段交互与局部草稿视图，或另行设计有生命周期的服务端草稿。二者选一个明确模型，勿双写。

三态按钮只改 data-state，没有 aria-pressed/radio 语义；输入没有关联等级 label，placeholder 不能替代稳定标签。JSON入口用 data-models-section，select() 却读 item.dataset.modelsPanel，aria-current 永不设置（探针确认）。`models-tag` / `is-grow` 在 TS 中使用，但 styles 中未找到对应定义，T 徽章不是先前声称的完整样式。

### F07 / P1–P2 / ❌ 目录浏览请求缺少共同同步域（源码）

`src/templates/dirs.html:4` 用 OOB 更新 dir-current；每个目录按钮独立 hx-get，`shell.html:177` 列表本身也发请求，没有 hx-sync。两个不同按钮可同时请求，迟到片段回写当前路径和列表；WorkBench 的 afterSwap 再无条件把它写进 cwd-input。

`workbench.ts:172` 提交直接用 cwd-input，不验证其等于最近成功浏览的目录；与先前研究的 Pi Web hasUncommittedPath 规则未对齐。输入期间迟到响应也会覆写正在输入的路径。

方向：浏览控件共用目录对话框 sync 域；明确“输入路径”与“已成功浏览路径”两份状态；请求失败、输入未确认时禁用“使用此目录”；必要的响应前守卫包含 OOB。父目录沙箱判定继续由桥提供。

### F08 / P1 / ⚠️ 请求归属守卫仍不完整（源码；与旧台账查重）

`workbench.ts:300` 比较的是最新 pendingHistory 与当前 epoch，不是**该 xhr 发起时**的 epoch；A→B→A 仍不可区分。旧 U10/U17 已标部分，不新算已修。

统计/系统/工具通过隐藏 sessionId 触发，但没有对应的响应归属守卫。A慢面板请求→切B，A可写入面板。workspace 只比 URL path，同目录回访也没有代次。旧 U06 可挡不同路径，不能扩称全覆盖。

方向：统一请求上下文（xhr → scope/revision），在 beforeOnLoad 丢弃过期响应，在切目标时 abort；同步域用 hx-sync。流式 WS/control 与 HTTP 读取分别管理，不给每个面板发明一套状态。

### F09 / P1 / ❌ 文件预览第二段 await 后缺守卫（源码）

`workspace.ts:162` files.image 返回后检查 generation，但后续 fetch(file-text)、response.text、WS fallback 及失败 note 都没有再检查。旧文件请求可在切目录/会话或关闭预览后 showPreview 并再次打开右栏。generation 变量已存在，职责却只做了一半。

方向：每个异步落地前核归属，关闭预览也使代次失效，fetch 配 AbortSignal。富文本/高亮保留浏览器增强，不强制把文件正文当 htmx HTML。

### F10 / P2 / ❌ 顶栏过渡被 hidden 立即切断（浏览器）

`app.css:16` 全局 `[hidden]{display:none!important}`；`:98–99` 顶栏 opacity/transform/visibility transition；`topbar.ts:37` 直接改 hidden。

实际构建 CSS 探针：打开后 getAnimations=0，关闭后=0，transitionrun=[]；display:none 直接结束渲染，visibility 延迟不能救回动画。前次“0.16s 淡入淡出已完成”结论不成立。

方向：选择一套能表达离场完成的方案（CSS离散过渡+兼容边界，或极小的状态/transitionend协调），同时确保关闭面板不可聚焦、reduced-motion立即结束。dialog当前keyframes只支持进入，不能宣称退出过渡齐全。

### F11 / P2 / ❌ 目录对话框样式被通用规则覆盖（浏览器）

`app.css:199` .dir-dialog 的520px、padding:0、overflow:hidden，被后面的 `:232` .app-dialog 同特异性覆盖。真实构建 CSS：

| 视口 | 实际宽度 | 实际 padding | 文件项字号 |
|---|---:|---:|---:|
| 1920×1080 | 460px | 24px | 12px |
| 1280×720 | 460px | 24px | 12px |
| 390×844 | 358px | 24px | 12px |

而且 shell 的 dialog 直接子节点是 form，不是各布局段；dialog 的 flex 不会让孙辈 dir-list 获得预期弹性空间。

.dir-dialog/.config-panel 无条件 display:flex 会覆盖 UA 对未 open 的 dialog 的 display:none。隔离探针 closed/open=false 时仍 display:flex；它可能位于整屏容器下方而不可见，**不是断言三个关闭弹窗都悬浮在用户眼前**。应恢复关闭态不参与布局/交互的契约，flex 限于 [open] 或内部布局容器。

### F12 / P2 / ❌ 文件项和会话列表重复定义，新增样式未生效（浏览器）

`app.css:53` 文件项11px、3px 6px padding；`:260` 旧版12px、6px 8px覆盖。三视口 computed 均为后者。

`:40` #session-list min-height:60px，被`:70` 的0覆盖。不是两套有意不同的组件，选择器完全相同。另有重复 panel-tabs transition、sidebar-splitter hover、config-actions（gap6→8）规则。

方向：删旧声明、每个组件一个基础块，变体显式作用域。检查最终样式，不再靠“类名有人用”证明规则无重复。

### F13 / P3 / ⚠️ CSS 结构与死规则回潮（源码）

`.models-dialog`、`.discover-grid`、`.config-kv-row/.kv-level` 已无当前模板/TS使用；models-dialog 是ID，不是class。旧自由文本映射的样式仍留在三态控件之前。

“响应式放最后”注释之后又新增 config、thinking、mc；并非每条晚规则都会出错，但文档结构约定已失真。`.tl-row[data-state=string] .config-detail ...` 与实际祖先顺序相反，是另一条正确选择器旁的无效分支。应按组件整理并降低通用表单规则特异性，避免继续用“加够类数”处理冲突。

### F14 / P2 / ⚠️ 侧栏上下分隔拖动公式与高度约束不一致（源码）

`workspace.ts:108–131` 每次move用**正在变化的文件区高度**作分母，却把比例施加到父侧栏高度；向下拖增加底部文件区高度，实际分界线向上移，方向也反。应固定起始父容器可用高度，按分界线方向计算，pointercancel/lostcapture收尾，读持久化值时夹到合法范围。CSS去除F12冲突后一起复核，别分两次补偿。

### F15 / P2 / ❌ 记忆完整正文与“加载更多”承诺未实现（源码）

`mc.html:54–70` 展开只有 Preview，长内容只加省略号；既没有正文 hx-get 也没有入口。当前桥 `transport/server.go:536` 只有 `/ui/mc` 分支，先前提到的 `/ui/mc/list`、`/ui/mc/memory` 不是已实现能力。

`workbench.ts:251–278` 计算 offset+50，再触发 mc-refresh 让整个 mc-body innerHTML 替换；按钮实际上是下一页，且只要 Rows非空就出现。结果会丢掉上一页和展开状态，也会在末页继续前往空页。

方向：Go模板输出真实下一步动作/hasMore；若叫加载更多，追加行并替换分页控件；分区/筛选替换列表，使用共同同步域；正文独立只读片段，授权与限额和列表一致。

### F16 / P2 / ⚠️ 模型表单未保持所有已支持值（源码）

`commitModel:352` 将 input 重写成 ['text','image'] 或删除，不保留原数组其它值；thinkingLevelMap只收七个固定键；正数检查未检查整数。与“其它自定义字段原样保留”的文案不同。需对桥支持的实际 schema 逐字段规定保留/拒绝/清除，不靠 UI 简化悄悄规范化。

### F17 / P2 / ⚠️ 生命周期没有跟上动态片段（源码）

`layout.ts:78` ResizeObserver 创建后不保存，dispose只 abort事件，不能disconnect观察器。`lazy.ts:17` 文档click委托无disposer，图片Blob只在load释放，error/移除路径未覆盖；无请求取消。`entry/app.ts:16` 富内容动态import后不判断root是否仍连接。Workspace启动import回调也需在dispose后阻止挂载。

方向：布局/模块各有disposer；片段组件接 beforeCleanupElement；图片成功、失败、取消均释放。WS增量、附件、终端继续由专属模块负责，别迁为服务端HTML来逃避生命周期。

### F18 / P2 / ⚠️ 扩展清单的 loaded 标志早于请求成功（源码）

`workbench.ts:152–156` 触发请求前即 packagesLoaded=true。第一次401/网络失败后切回扩展不再自动拉取；手动“重新读取”仍可恢复。状态应在成功交换后确认，失败允许再试。

### F19 / P3 / ⚠️ 格式与文档漂移（源码）

`topbar.ts:32` formatCompact仍输出大写K，桥侧统计用小写k；不能把前次面板修正描述成全局已对齐。

components.md仍写预算24/42（manifest已30/50）、72测试（当前148）、会话信息保留运行组（后文又说已删除）、已迁移顶栏仍列迁移候选、Tailwind扫描工具类等。历史记录可保留，当前状态须从独立“现状”段给出，避免相互冲突。

## 4. 合理保留的部分

✅ 系统/工具/统计、历史、分支、文件列表、Git片段由桥渲染。工具选中项、复制按钮不需要再发请求。

✅ 原生details、用户消息条件fork、服务端usage、CSS变量控制本地偏好、剪贴板与附件留客户端。

✅ 文件文本fetch + 高亮/ANSI与图片Blob不是“伪htmx”；真正问题是取消/归属。WS控制协议不为形式统一强行换成HTTP。

✅ htmx allowEval/allowScriptTags关闭、historyCacheSize=0；主题单源、高亮透明底、reduced-motion兜底仍成立。

## 5. 验证、覆盖限制与处理顺序

- 当前基线：17个测试文件/148测试通过，typecheck、pnpm check通过，退出码逐步确认。
- 隔离审计探针6项通过，**断言的是当前坏行为确实存在**，不是修复验收：切换丢字段、重名覆盖、监听增长、末项删除无法保存、保存覆盖新草稿、JSON选中态缺失。探针保存在 `/srv/projects/agentTmp/pi-frontend-review/audit-probes.test.ts`，临时测试已从产品仓移走。
- agent-browser使用独立session和**现有构建的CSS**，覆盖1920×1080、1280×720、390×844计算样式及顶栏transition事件。是隔离DOM，不是完整应用端到端/axe验收；不宣称已测试所有交互/所有主题。
- 没有新增“镜像实现”的绿色回归来粉饰问题，也未修改产品代码。

后续建议按依赖处理：

1. F01–F06/F16：先确立配置草稿与保存契约，再调整HTML归属，防止为htmx再重写一遍。
2. F07–F09/F18：共同请求作用域与同步策略；与旧U06/U10/U17/U18查重。F01对应U19回归。
3. F10–F14：CSS级联/原生dialog状态/布局容器/拖动一起核查，三个视口加键盘/reduced-motion。
4. F15：记忆分页与全文服务端片段；不增加前端行渲染器。
5. F17/F19：生命周期收尾与现状文档同步。

以上是审查时的状态，以下记录实施结果。

## 6. 跨仓落地与验收（2026-09-30）

配套桥提交：`fe68e77`。新片段需与本轮UI模板一起部署；保留原有JSON/WS调用兼容。

| 条目 | 结果 | 实现与证据 |
|---|---|---|
| F01 | ✅ | 节点/JSON切换先收集到单一草稿；保存独立快照，不以重读覆盖后续输入；再次打开保留当前表单；在途保存合并 |
| F02 | ✅ | 供应商重名在写盘前拒绝，`__proto__`等合法键用无原型字典保留；含打码凭据的重命名明确拒绝隐式秘密迁移；桥继续按原身份校验/恢复秘密 |
| F03 | ✅ | 保存整个草稿，删除末项后空 providers 可保存，无须选择节点 |
| F04 | ✅ | 固定容器只绑定一次委托，随 ModelsEditor.dispose 释放 |
| F05 | ✅ | 发现/测试改 POST HTML片段；提供者/输入/关闭使请求归属失效，hx-sync replace 取消旧请求；桥复用已有出站服务与操作槽 |
| F06 | ✅ 明确例外 | 固定七行控件来自 HTML template，发现列表归 Go；草稿树留客户端，避免为未保存内容再造服务器草稿。三态 aria-pressed、输入等级标签、JSON选中态和T徽章均补齐 |
| F07 | ✅ | 目录共同同步域；输入/请求路径与成功浏览路径分开；xhr守卫先于OOB；在途、未浏览与失败状态均禁止提交。失败片段不会回填旧路径 |
| F08 | ✅ | 共用 xhr→目标/会话epoch/局部revision 映射；beforeOnLoad拒绝旧响应，切会话主动取消会话片段。配置草稿和记忆面板不随聊天会话被取消 |
| F09 | ✅ | 文件文本请求有 AbortController，每个异步落地检查代次，关闭预览立即失效；延迟Response反例确认不会重新打开 |
| F10 | ✅ 明确语义 | 顶栏160ms进入动画、关闭立即隐藏；实测getAnimations返回panel-fade-in/160ms。没有宣称退出动画；reduced-motion保留全局关闭 |
| F11 | ✅ | dialog仅open时flex，目录form承担弹性高度；目录专属宽度/padding不再被通用规则覆盖。全部关闭dialog的computed display为none |
| F12 | ✅ | 删除文件项/会话列表/控件重复基础规则；真实页面文件项恢复11px |
| F13 | ✅ | 清除旧模型/发现/映射死规则；响应式与无障碍统一在末尾；通用字段用:where降低权重，紧凑控件无需层叠祖先补偿 |
| F14 | ✅ | 拖动分母为起始父容器内容高度（扣padding），修正方向、持久化夹值及lostcapture；真实鼠标向下50px，分隔线556.109→606.109px，文件区473→423px |
| F15 | ✅ | 模板声明分区/筛选/分页；追加行、替换分页控件、末页停止。新增只读正文端点；修正directives/dreams真实表与列映射、数字主键；内容上限65536字符；同时间按ID排序 |
| F16 | ✅ | 未更改input能力/false/未来映射键/空字符串均保留；前端与桥都拒绝非正数/小数Token限额 |
| F17 | ✅ | ResizeObserver可disconnect；图片Blob成功/失败/清理/卸载都释放，请求可取消；动态import完成后检查挂载作用域；思考正文已改为Go HTML+hx-get |
| F18 | ✅ | 扩展loaded只认成功交换的清单标记，401/网络失败/状态说明片段允许再次尝试 |
| F19 | ✅ | 顶栏小写k与桥一致；components现状、预算、测试数、迁移清单已同步，历史数据保留日期 |

原需求⑧一并落实到位置：编辑按钮在用户气泡下，助手回合尾部只保留复制；仍是分支后预填草稿，不冒充Pi Web原地改叶子。

### 跨仓边界

- 新 HTTP 模型查询仍先过认证、Host/Origin，表单64KiB上限，使用全局操作槽。`***` 不会作为真实密钥发往供应商，`!command` 不执行；httptest 上游确认 HTML 转义与未认证401。
- 模型保存提示移到全部编辑视图可见的位置，不能把表单错误藏在JSON分节。配置仍是整文档原子写入；v1未增加跨浏览器/CLI的CAS。
- 思考 `format=html` 自动转义，原JSON/图片协议保持可用；lazy和文件全文错误保留真实状态码。记忆正文只读、无缓存，归档memory不能绕列表读取。
- 桥 O01–O04 的性能原型不混入本轮，Git不改。

### 验证

- UI：21文件/161项测试；typecheck、build、契约、5主题对比度均通过。反例覆盖草稿丢失/重名/末项删除/保存期间继续编辑/重复委托/未知字段、xhr A→B→A、目录失败、迟到预览、Blob卸载。
- 桥：`PI_WEBUI_DIR` 指向本次UI，`go test -race -count=1 ./...`、vet、staticcheck及gofmt通过；新增认证、HTML转义、记忆追加/尾页/全文、思考HTML与旧JSON共存测试。
- 使用隔离桥+合成models.json/会话/context.db做真实浏览器联测，不读取生产秘密、不调用真实模型。保存后磁盘模型名称改变、另一个provider仍保留；记忆50→63条后移除下一页，筛选1条，完整正文实际加载；思考中的`<script>`显示为文本，DOM里script数0。

| 视口 | 目录弹窗 | 模型弹窗 | 横向溢出 |
|---|---|---|---|
| 1920×1080 | 520×620、padding0 | 900×842 | 无 |
| 1280×720 | 520×620、padding0 | 900×562 | 无 |
| 390×844 | 356×620、padding0 | 356×828，上下布局 | 无 |

紧凑映射输入框仍19px高。首屏自有 **28.10/30 KiB gzip**、总计 **45.69/50 KiB**，未新增依赖。截图及命令日志在 `/srv/projects/agentTmp/pi-frontend-review/`（final-1920.png、final-1280.png、final-390.png）。时序反例由可控延迟单测验证；没有宣称真实模型/公网部署、所有交互的全浏览器自动回放或全主题axe验收。
