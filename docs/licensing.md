# 许可证核查：能否采用 AGPL-3.0

结论先行：**可以**，我们自己的代码可以按 AGPL-3.0 发布；但有一条**具体边界**需要处理——
前端产物里的图表库 mermaid 会带进一个 **EPL-2.0** 的传递依赖（`elkjs`），而 EPL-2.0
与 GPL/AGPL 系列不兼容。

本文是工程核查记录，不是法律意见。发布前若涉及商业用途，请自行确认或咨询专业人士。

## 1. 核查方法（可复现）

```bash
# Go 侧：列出真的编译进二进制的模块（排除测试依赖）
cd pi-bridge-go
go list -deps -f '{{if .Module}}{{.Module.Path}} {{.Module.Version}}{{end}}' ./cmd/pi-bridge | sort -u

# 前端：扫描 node_modules 下全部包的 license 字段
cd pi-webui-htmx
python3 - <<'PY'
import json,glob,collections
lics=collections.Counter()
for f in glob.glob('node_modules/.pnpm/*/node_modules/*/package.json'):
    d=json.load(open(f)); l=d.get('license') or d.get('licenses') or ''
    if isinstance(l,list): l=' OR '.join(str(x) for x in l)
    elif isinstance(l,dict): l=l.get('type','')
    lics[str(l)]+=1
for l,c in lics.most_common(): print(c,l)
PY
```

## 2. Go 侧：全部兼容

实际编译进 `pi-bridge` 的模块只有 4 个外部模块（另有 1 个仅作为 brotli 的测试依赖）：

| 模块 | 许可证 | 与 AGPL-3.0 兼容 |
|---|---|---|
| `github.com/andybalholm/brotli` | MIT | ✅ |
| `github.com/coder/websocket` | ISC | ✅ |
| `github.com/creack/pty` | MIT | ✅ |
| `golang.org/x/mod` | BSD-3-Clause | ✅ |
| `github.com/xyproto/randomstring` | BSD-3-Clause | ✅（不进制物） |

Go 标准库按 BSD-3-Clause 分发，同样兼容。**Go 侧没有许可证障碍。**

## 3. 前端：主流宽松，一个例外

`node_modules` 下 369 个包（含构建工具）的许可证分布：

| 许可证 | 数量 | 兼容 AGPL-3.0 |
|---|---|---|
| MIT | 194 | ✅ |
| ISC | 110 | ✅ |
| BSD-3-Clause | 20 | ✅ |
| Apache-2.0 | 13 | ✅ |
| MPL-2.0 | 10 | ✅（文件级 copyleft，可作 Larger Work 组合） |
| BSD-2-Clause | 6 | ✅ |
| BlueOak-1.0.0 | 4 | ✅ |
| MPL-2.0 OR Apache-2.0 | 3 | ✅（可选 Apache-2.0） |
| **EPL-2.0** | **2** | ❌ **与 GPL/AGPL 不兼容** |
| Unlicense / CC0-1.0 | 4 | ✅（视同公有领域） |

### 唯一的例外：elkjs（EPL-2.0）

`elkjs@0.9.3` 是 **mermaid 的硬依赖**（`mermaid@12.0.0` → `elkjs ^0.9.3`），
而 mermaid 本身是 MIT。它进入前端产物的证据：

```
pi-webui-htmx/dist/assets/elk-276RUBZZ-BSKOscbQ.js   1.4 MB
```

`src/modules/mermaid.ts` 用 `import('mermaid')` **动态加载**（只在正文出现图表代码块时触发），
但该 chunk 会随 `dist/` 一起分发。

**不兼容的理由**：EPL-2.0 的专利条款与再许可限制使它无法与 GPL-3.0/AGPL-3.0 组合——
这是 FSF 许可证列表与 Eclipse 官方 FAQ 一致的公开立场，不是我们的判断。

其余几个非宽松许可都不构成问题：`lightningcss`（MPL-2.0）是 Tailwind 的构建工具、不进产物；
`dompurify` 是双许可，选 Apache-2.0 即可。

## 4. 上游参考物的许可证

| 项目 | 许可证 | 对我们选择的影响 |
|---|---|---|
| `earendil-works/pi`（本体的 RPC 子进程） | MIT | 无。桥通过进程边界调用它，不嵌入、不分发其代码 |
| `agegr/pi-web`（对照实现） | MIT | 无。我们是独立实现（Go 模板 + HTMX），只对齐交互行为 |

## 5. 三个可选方案

| 方案 | 做法 | 代价 |
|---|---|---|
| **A. 发布源码，不发布构建产物**（推荐起步） | 保持 `dist/` 与 `node_modules/` 不入库；仓库里只有源码与依赖声明，**不含 EPL 代码**；README 写明「构建产物含 EPL-2.0 的 elkjs」 | 用户必须自行 `pnpm build`；若你将来发布 Releases/Docker 镜像，需回到方案 B/C |
| **B. 移除 mermaid** | 删掉 `src/modules/mermaid.ts` 与依赖 | 失去图表渲染（KaTeX 数学公式是 MIT，不受影响） |
| **C. 把 mermaid 改成可选运行时依赖** | 不打包，由部署方自行提供 | 需要架构改动；与项目「无 CDN 兜底」的原则冲突 |

**区分两种行为**（这是关键，不是文字游戏）：

- **分发源码**：仓库里没有 EPL 代码，只有 `package.json` 里一行依赖声明。依赖声明本身不受 copyleft 传染。
- **分发构建产物**（发布 `dist/`、Docker 镜像、把 UI 内嵌进二进制）：此时 EPL-2.0 代码实际随你分发，
  方案 A 的免责说法不再成立。

## 6. AGPL-3.0 的实际约束（给部署者的提醒）

选 AGPL 通常是为了防止他人拿去做闭源 SaaS，但要知道它对自己的约束：

- **第 13 节（网络交互）**：任何通过网络与你部署的实例交互的用户，都有权获得对应版本的完整源代码。
  自己本机用不触发；把它开放给他人使用就触发。
- **衍生作品同样要 AGPL**：别人修改后分发或提供网络服务，必须同样开源。
- **与 GPL-3.0 兼容**：可以与 GPLv3 代码组合；与 GPLv2-only 不兼容。

如果目标只是「保留署名、不限制别人怎么用」，用 Apache-2.0 或 MIT 更省事，且能避开第 3 节的 EPL 问题。
