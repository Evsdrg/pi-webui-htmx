#!/usr/bin/env node
// 检查模板结构与生产产物；交互接线由 tests/unit 和浏览器验收负责。
import { readFileSync, existsSync, readdirSync } from 'node:fs';
import { dirname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';
const root=dirname(dirname(fileURLToPath(import.meta.url)));
let failed=0;
const fail=message=>{console.error(`✗ ${message}`);failed++;};
const ok=message=>console.log(`✓ ${message}`);
const manifest=JSON.parse(readFileSync(resolve(root,'ui-manifest.json'),'utf8'));
for(const field of ['uiVersion','protocolVersion','piBaseline','templates','routes','requiredMethods','extensionChannel','build','assets']){if(manifest[field]===undefined)fail(`manifest 缺少 ${field}`);}
for(const [name,relative] of Object.entries(manifest.templates??{})){
 if(name.startsWith('_'))continue;
 const path=resolve(root,'src',relative);
 if(!path.startsWith(resolve(root,'src')+sep)||!existsSync(path)){fail(`模板路径无效: ${name}`);continue;}
 const body=readFileSync(path,'utf8');
 // export 是独立下载文档（完整 HTML），与 shell 同类；其余必须是片段。
 const fullDoc=name==='shell'||name==='export';
 if(!fullDoc&&/<!doctype|<(?:html|head|body)[\s>]/i.test(body))fail(`片段 ${name} 包含完整文档结构`);
 if(!fullDoc&&/<script[\s>]/i.test(body))fail(`片段 ${name} 含脚本`);
 // 导出文档也不允许脚本：Pi Web 的导出内嵌递归树脚本，长链会栈溢出（B45）。
 if(name==='export'&&/<script[\s>]/i.test(body))fail('导出文档不得含脚本');
 if(/style="[^"]*\{\{/.test(body))fail(`模板 ${name} 含动态内联样式`);
 if(name==='extDialog'&&(!body.includes('data-dialog-id')||!body.includes('data-extension-form')))fail('扩展对话缺少回执表单标记');
 ok(`模板 ${name}`);
}
// 工具块/思考块的字号只能声明一次：这两条外形规则在 B 批对齐过 Pi Web
// （11px）。多写一条同特异性的 font-size 会静默压过去，界面看起来「只是大一点」。
{
 const css=readFileSync(resolve(root,'src/styles/app.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 const decls=css.split('}').flatMap(block=>{
  const [selector,body]=block.split('{');
  if(!selector||!body)return [];
  return /(^|,)\s*\.tool-call\s*(,|$)/.test(selector)&&/font-size\s*:/.test(body)?[body.match(/font-size\s*:[^;]+/)[0]]:[];
 });
 if(decls.length!==1||!/11px/.test(decls[0]))fail(`.tool-call 的 font-size 应只声明一次且为 11px，实际 ${JSON.stringify(decls)}`);
 ok('工具块字号只有一处声明');
}

// 消息滚动容器必须预留滚动条槽。经典（占位式）滚动条会随内容高度出现/消失，
// 每次出现都从内容区夺走一条竖直槽、把整片文字挤窄并触发重排；切换会话（清空
// 再填入）、翻页、流式增高都会让它来回切，肉眼就是「切会话时左右闪」。这条
// 守卫挡住无意中删掉 scrollbar-gutter:stable 而让闪烁回归。
{
 const css=readFileSync(resolve(root,'src/styles/app.css'),'utf8');
 const m=css.match(/\.chat-scroll\s*\{([^}]*)\}/);
 if(!m||!/scrollbar-gutter\s*:\s*stable/.test(m[1]))fail('.chat-scroll 缺少 scrollbar-gutter:stable：滚动条出现/消失会改变内容宽度并引发整片重排，切换会话时会来回闪');
 else ok('消息滚动容器预留滚动条槽（切会话不因滚动条重排）');
}

// 模板里的请求 URL 必须是相对的（B54）：云端形态下文档在
// relay 的 `/d/{deviceId}/` 前缀下，根绝对路径会绕过设备前缀。
// 允许的例外是以 `{{` 开头（由桥注入）或已带前缀的写法。
{
 const dir=resolve(root,'src/templates');
 const files=[];
 const walk=(d)=>{for(const e of readdirSync(d,{withFileTypes:true})){const p=resolve(d,e.name);e.isDirectory()?walk(p):e.name.endsWith('.html')&&files.push(p);}};
 walk(dir);
 for(const file of files){
  const body=readFileSync(file,'utf8');
  // 覆盖一切会**发起请求**的属性：hx-*、action、href、src。
  // 只查 hx-* 曾经漏掉 href="/?session=…"——在设备前缀形态下（relay 的
  // /d/{id}/）点会话会跳出前缀，而 caddy 反代形态的前缀就是 "/"，
  // 本地与反代都测不出来（B54）。
  for(const mm of body.matchAll(/\b(hx-(?:get|post|put|delete)|action|href|src)="(\/[^"]*)"/g)){
   fail(`${file.replace(root+'/','')} 里的 ${mm[1]} 是根绝对路径：${mm[2]}（应写成相对路径）`);
  }
 }
 ok('模板请求 URL 都是相对路径');
}

// app.css 里引用的自定义属性必须在 tokens.css 或 app.css 里有定义。
// 踩过的坑：--font-mono 被 6 处 var() 引用却从未定义，
// 那些 font-family 全部静默失效、回落到继承字体——界面上看不出错。
{
 const files=[['tokens.css',readFileSync(resolve(root,'src/styles/tokens.css'),'utf8')],
              ['app.css',readFileSync(resolve(root,'src/styles/app.css'),'utf8')],
              ['code.css',readFileSync(resolve(root,'src/styles/code.css'),'utf8')]];
 const defined=new Set();
 for(const [,body] of files)for(const mm of body.replace(/\/\*[\s\S]*?\*\//g,'').matchAll(/(--[a-z0-9-]+)\s*:/g))defined.add(mm[1]);
 const used=new Set();
 for(const [name,body] of files)for(const mm of body.replace(/\/\*[\s\S]*?\*\//g,'').matchAll(/var\(\s*(--[a-z0-9-]+)/g))used.add(mm[1]);
 const missing=[...used].filter(name=>!defined.has(name));
 if(missing.length)fail(`以下自定义属性被引用但从未定义：${missing.join('、')}`);
 ok('自定义属性引用都有定义');
}

// 顶栏的右对齐必须由容器承担。踩过的坑：`margin-left:auto` 挂在 #context-usage
// 上，而它在没有上下文数据时是 `hidden`（`[hidden]{display:none}`）——
// 那时 auto 外边距完全失效，「就绪」与右侧按钮就跟着工具栏跑到栏中间了。
// 规律：对齐锤不能挂在「可能被 hidden、且后面还有依赖该对齐的兄弟节点」的元素上。
{
 const shell=readFileSync(resolve(root,'src/templates/shell.html'),'utf8');
 const css=readFileSync(resolve(root,'src/styles/app.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 const topbar=/<header class="topbar">([\s\S]*?)<\/header>/.exec(shell)?.[1];
 if(!topbar)fail('shell.html 里找不到顶栏');
 else{
  if(!/<div class="topbar-right">/.test(topbar))fail('顶栏缺少 .topbar-right 容器（右对齐需要一个不隐藏的锚点）');
  else ok('顶栏右对齐由 .topbar-right 容器承担');
  // 顶栏内带 hidden 的元素不得靠 margin-left:auto 定位。
  const anchored=[...css.matchAll(/([^{}]*)\{([^}]*margin-left\s*:\s*auto[^}]*)\}/g)].map(match=>match[1]).join(',');
  for(const tag of topbar.matchAll(/<(\w+)([^>]*\bhidden\b[^>]*)>/g)){
   const cls=(/class="([^"]*)"/.exec(tag[2])?.[1]??'').split(/\s+/).filter(Boolean);
   for(const name of cls){
    if(new RegExp(`\\.${name.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')}(?![\\w-])`).test(anchored))fail(`顶栏里带 hidden 的 .${name} 同时靠 margin-left:auto 定位，隐藏时对齐会失效`);
   }
  }
 }
}
// 会话详情的三组列模板是对齐 Pi Web 的关键，而它们只比特异性、不比意图：
// 曾经有一条 `.stats-grid > .stats-section > .stats-rows`（3 个类）压过
// `.stats-token .stats-rows`（2 个类），Token 段的值列因此从「贴内容」
// 变成「吃满剩余空间」，数字被推到面板最右侧，与 Pi Web 的观感不同。
// 这里把「谁决定列模板」锁死，让这种覆盖无法悄悄回归。
{
 // 先去掉注释再解析选择器：注释会粘进选择器文本，让比对永远失败。
 const css=readFileSync(resolve(root,'src/styles/app.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 const declaring=[];
 for(const match of css.matchAll(/([^{}]+)\{([^}]*)\}/g)){
  if(/grid-template-columns/.test(match[2])&&/stats-rows|stats-grid|stats-section/.test(match[1]))declaring.push(match[1].trim());
 }
 const expected=['.stats-grid','.stats-info .stats-rows','.stats-message .stats-rows','.stats-token .stats-rows'];
 const sorted=[...new Set(declaring)].sort();
 if(JSON.stringify(sorted)!==JSON.stringify(expected))fail(`会话详情的列模板只应由这些规则声明：${expected.join(' / ')}；实际为 ${sorted.join(' / ')||'（无）'}`);
 else ok('会话详情列模板由分组规则独占');
 // 通用 `.stats-rows` 一旦声明列模板，就会与分组规则争特异性（正是踩过的坑）。
 const base=/(?:^|\})\s*\.stats-rows\s*\{([^}]*)\}/m.exec(css)?.[1]??'';
 if(/grid-template-columns/.test(base))fail('通用 .stats-rows 不得声明列模板，否则会覆盖分组规则');
 const body=selector=>new RegExp(`(?:^|\\})\\s*${selector.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')}\\s*\\{([^}]*)\\}`,'m').exec(css)?.[1]??'';
 const token=body('.stats-token .stats-rows');
 if(!/grid-template-columns\s*:\s*max-content max-content/.test(token)||!/justify-content\s*:\s*start/.test(token))fail('Token 组必须是 max-content 两列且整组靠左（Pi Web 的 compact 形态）');
 if(!/text-align\s*:\s*right/.test(body('.stats-token .stats-rows dd')))fail('Token 组的值需在窄列内右对齐');
 if(!/grid-template-columns\s*:\s*auto minmax\(0,\s*1fr\) auto/.test(body('.stats-info .stats-rows')))fail('会话事实组需要标签/值/复制按钮三列');
}
// 片段里的组件 class 必须有样式。踩过的坑：`.packages` 表格、`.tag-warn`/
// `.tag-err`/`.tag-add`/`.tag-del` 状态标签、`.mono` 输入框都被模板引用，却
// 从未在任何 CSS 里定义——表格以默认外观渲染，标签丢失配色，输入框不等宽。
// 这类「引用了却没定义」在功能测试里不报错，只有肉眼才发现，必须拦住。
//
// 规则：模板 class="…" 里的每个类，要么在样式表（含模板内联 <style>）里有定义，
// 要么显式登记在 UNSTYLED_HOOKS 里（纯 JS 选择器钩子，或纯粹的分组容器，视觉上
// 无需样式）。这样新增一个「以为有样式、其实没有」的类会立刻失败。
{
 const styleFiles=['tokens.css','app.css','code.css','models.css'];
 const styled=new Set();
 for(const f of styleFiles){
  const css=readFileSync(resolve(root,'src/styles',f),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
  for(const mm of css.matchAll(/\.([a-zA-Z_][\w-]*)/g))styled.add(mm[1]);
 }
 const dir=resolve(root,'src/templates');
 const bodies=[];
 const walk=(d)=>{for(const e of readdirSync(d,{withFileTypes:true})){const p=resolve(d,e.name);e.isDirectory()?walk(p):e.name.endsWith('.html')&&bodies.push(readFileSync(p,'utf8'));}};
 walk(dir);
 // 模板内联 <style> 里的定义同样算已定义（export.html 用它自带给导出文档的样式）。
 for(const body of bodies)for(const sm of body.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g))
  for(const cm of sm[1].matchAll(/\.([a-zA-Z_][\w-]*)/g))styled.add(cm[1]);
 // 有意不做样式的类：纯 JS 选择器钩子，或只是给子元素分组的容器。
 // 每个都要写清理由，避免这里变成「随手加一行就绕过守卫」的后门。
 const UNSTYLED_HOOKS=new Set([
  'diff',            // 变更片段的根容器，仅作定位
  'diff-line',       // 行级 JS/结构钩子；配色由 diff-add/diff-del 给
  'diff-no-new','diff-no-old', // 新旧行号列，样式走共享的 .diff-no
  'file-node',       // 目录行容器，样式在 .file-item/.file-children
  'stats-copy',      // 会话详情里放复制按钮的网格列
 ]);
 const missing=new Set();
 for(const body of bodies)for(const mm of body.matchAll(/class="([^"]*)"/g)){
  const cleaned=mm[1].replace(/\{\{[^}]*\}\}/g,' ');
  for(const tok of cleaned.split(/\s+/).filter(Boolean)){
   if(/[{}]/.test(tok))continue;
   // 去掉模板插值后可能留下的残缺标记（如 `diff-{{.Kind}}` → `diff-`）。
   if(tok.endsWith('-')||tok==='is')continue;
   if(styled.has(tok)||UNSTYLED_HOOKS.has(tok))continue;
   missing.add(tok);
  }
 }
 if(missing.size)fail(`这些 class 被模板引用，却既无样式定义、也未登记为钩子：${[...missing].sort().join('、')}（如确为纯钩子，请加入 check-contract.mjs 的 UNSTYLED_HOOKS 并写明理由）`);
 else ok('模板组件类都有样式定义或已登记为钩子');
}
// 反向守卫：CSS 里定义的组件类必须被「某一方」引用——模板、TS、JS 运行时拼名
// 或桥渲染时注入，四路都不出现即为死规则。踩过的坑：`.toast-success` 的 kind 在
// workbench 里被收窄成 error/warning/info 后永远不会再产生，样式却一直留着；
// `.state-online` 在模板改版后成了孤儿。死规则的危害不是体积（每条几十字节），
// 而是「改样式的人以为它还在生效」——对着一段不渲染的 CSS 调参数，怎么调都没反应。
//
// 规则：app.css/models.css 顶层类（拆掉属性选择器与状态后缀取主干）至少被下列
// 之一引用，否则必须登记到 DEAD_ALLOWED 并写明理由：
//   a) src/templates 静态 class（{{…}} 插值先抹掉）；
//   b) TS/JS 字符串（模板字面量拼名如 `toast toast-${kind}` 按前缀覆盖）；
//   c) 动态拼名白名单 RUNTIMEClassFamilies：由枚举值拼出的类（桥 Go 常量 / TS 联合类型），
//      前缀登记 + 枚举展开核对，防止「拼名来源改了枚举」静默断链；
//   d) htmx/浏览器运行时行为（htmx-request 等），单独列理由。
{
 const styleFiles=['app.css','models.css'];
 const templateBodies=[];
 {
  const dir=resolve(root,'src/templates');
  const walk=(d)=>{for(const e of readdirSync(d,{withFileTypes:true})){const p=resolve(d,e.name);e.isDirectory()?walk(p):e.name.endsWith('.html')&&templateBodies.push(readFileSync(p,'utf8'));}};
  walk(dir);
 }
 // 模板侧引用：先抹 {{…}}（它是服务端注入，抹掉后的残词如 `diff-`、`is-` 不算引用）。
 const templateRef=(name)=>templateBodies.some(b=>b.replace(/\{\{[^}]*\}\}/g,' ').includes(name));
 const tsRef=(name)=>{
  for(const f of ['src/modules','src/entry','src/lib']){
   const d=resolve(root,f);
   const walk=(dir)=>{for(const e of readdirSync(dir,{withFileTypes:true})){const p=resolve(dir,e.name);if(e.isDirectory())walk(p);else if(e.name.endsWith('.ts')&&readFileSync(p,'utf8').includes(name))return true;}return false;};
   if(walk(d))return true;
  }
  return false;
 };
 // 动态拼名家族：主干 → 允许的后缀枚举与出处。核对「CSS 里出现的主干+后缀组合」
 // 是否都在枚举内，枚举外即失败——这挡的是「枚举加了新值而样式没跟上」或反向。
 const RUNTIME_CLASS_FAMILIES={
  // 拼名来源核对记录（2026-10-18，逐一对过产生方源码）：
  // toast：toast.ts `toast toast-${kind}`；kind 联合类型含 'success'（协议允许），
  //   但 workbench.notify 的白名单把它折成 info，故 success 目前不产生（见 DEAD_ALLOWED）。
  'toast':{from:'toast.ts `toast-${kind}`（NotifyKind）',suffixes:['','root','info','success','warning','error','exit','in']},
  // state：workbench.ts:1390 `state state-${online?'online':'offline'}` + 模板静态 state-offline。
  'state':{from:'workbench.ts setConnection 拼状态类 + 模板 state-offline',suffixes:['','online','offline']},
  // diff：diff.html:18 `diff-line diff-{{.Kind}}`；Kind 枚举见
  //   pi-bridge-go/internal/presentation/workspace.go:91-100（hunk/add/del/context）。
  'diff':{from:'diff.html `diff-{{.Kind}}`，Kind 由 workspace.go 枚举 add/del/hunk/context',suffixes:['','line','file','file-head','path','table','no','text','add','del','hunk','context']},
  // row：export.html:54 `row-{{.Kind}}`；Kind 枚举见
  //   pi-bridge-go/internal/sessions/export.go:18（user/assistant/thinking/toolCall/toolResult/info），
  //   样式内联在 export.html 的 <style>（check-contract 视其已定义，家族核对仍覆盖）。
  'row':{from:'export.html `row-{{.Kind}}`，Kind 由 sessions/export.go 枚举 user/assistant/thinking/toolCall/toolResult/info',suffixes:['','user','assistant','thinking','toolCall','toolResult','info']},
  // is-*：mc.html:2 `mc-tag is-{{.Status}}`（status 来自 magic-context 库的
  //   status 列：active/archived/dismissed/pending/dropped）；is-running（模板+TS）、
  //   is-collapsed（workspace.ts toggle）、is-primary/is-danger（模板静态）、
  //   is-grow（models.css 实用类）。
  'is':{from:'mc.html `mc-tag is-{{.Status}}` + 模板/TS 的 is-running、is-collapsed、is-primary、is-danger、is-grow',suffixes:['','running','collapsed','active','archived','superseded','dismissed','pending','dropped','grow','primary','danger']},
 };
 // htmx/浏览器运行时会自己挂上的类：不在我方任何源码里出现。
 const RUNTIME_CLASSES=new Set([
  'htmx-request', // htmx.js addRequestIndicatorClasses：未配 hx-indicator 时加到发起元素（app.css 慢面板载入提示依赖它）
  // tag-ok：桥渲染 mcp.html 时经 `class="tag {{.StateClass}}"` 注入
  // （pi-bridge-go/internal/transport/server.go mcpStateLabel：connected→tag-ok）。
  // 模板侧只见插值占位符，前端源码里没有字面量，归入桥注入豁免并附出处。
  'tag-ok',
 ]);
 // 经核实确实无调用方的存量（如需新增必须附核实记录，格式：类名 — 何时因何保留）。
 const DEAD_ALLOWED=new Set([
  // （空）2026-10-18 清理过 .state-online 与 .toast-success 两条死规则，
  // 此处留作后续临时豁免的登记位；长期无成员是常态。
 ]);
 const definitions=[]; // {name, at}  at: 类名在 CSS 出现的行（报错定位用）
 for(const f of styleFiles){
  const css=readFileSync(resolve(root,'src/styles',f),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
  for(const mm of css.matchAll(/\.([a-zA-Z][\w-]*)/g))definitions.push({name:mm[1],at:`${f}:${mm[1]}`});
 }
 // 家族核对：CSS 里每个 `主干-后缀` 组合必须落在枚举内。
 for(const d of definitions){
  for(const [stem,fam] of Object.entries(RUNTIME_CLASS_FAMILIES)){
   if(d.name===stem||d.name.startsWith(stem+'-')){
    const suffix=d.name===stem?'':d.name.slice(stem.length+1);
    if(!fam.suffixes.includes(suffix))fail(`CSS 类 .${d.name} 属于动态拼名家族「${stem}-」（${fam.from}），但后缀「${suffix}」不在枚举里——拼名来源改枚举时请同步本清单与样式`);
   }
  }
 }
 const dead=definitions
  .filter(d=>{
   const name=d.name;
   if(DEAD_ALLOWED.has(name)||RUNTIME_CLASSES.has(name))return false;
   // 家族成员不走死活判定（上面 EnumRange 核对已覆盖「后缀越界」），只判非家族类。
   const inFamily=Object.keys(RUNTIME_CLASS_FAMILIES).some(stem=>name===stem||name.startsWith(stem+'-'));
   return !inFamily&&!templateRef(name)&&!tsRef(name);
  })
  .map(d=>d.name);
 if(dead.length)fail(`这些类在 CSS 里定义，却没有任何模板/TS/运行时引用（死规则）：${[...new Set(dead)].sort().join('、')}。确认无调用方就删掉样式；确需保留（如为下一步改动铺垫）请登记 check-contract.mjs 的 DEAD_ALLOWED 并附核实记录`);
 else ok('CSS 类都有引用方或已登记豁免');
}
// 主题契约：主题 id 由 theme.ts 解析后写 data-theme，
// 因此这里核对三件事，全部是「静默分叉」型故障的入口：
//   1. theme.ts 声明的主题 id 与 tokens.css 的块一一对应（light 用 :root）；
//   2. 夜间主题必须定义完整调色板——漏一个令牌
//      就会在深色页面上露出对应的浅色 fallback（例如工具块白底）；
//   3. tokens.css 不得再出现 prefers-color-scheme 深色块：换主题的唯一入口
//      是 JS 解析（system 模式由 matchMedia 监听重解析），两份入口会分叉。
{
 const layout=readFileSync(resolve(root,'src/modules/theme.ts'),'utf8');
 const list=(name)=>{
  const match=new RegExp(`${name}\\s*=\\s*\\[([^\\]]+)\\]`).exec(layout);
  if(!match)fail(`theme.ts 缺少 ${name} 数组`);
  return [...(match?.[1]??'').matchAll(/'([a-z-]+)'/g)].map(m=>m[1]);
 };
 const lightThemes=list('LIGHT_THEMES'),darkThemes=list('DARK_THEMES');
 if(!lightThemes.length||!darkThemes.length)fail('主题清单为空');
 const css=readFileSync(resolve(root,'src/styles/tokens.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 if(/prefers-color-scheme/.test(css))fail('tokens.css 又出现了 prefers-color-scheme 深色块（主题只有 JS 解析一个入口）');
 const block=(id)=>new RegExp(`\\[data-theme="${id}"\\]\\s*\\{([^}]*)\\}`).exec(css)?.[1];
 // 深色调色板的必需令牌：与 :root（light）那批同名，覆盖到 theme 的一切。
 const required=['--bg','--bg-panel','--bg-hover','--bg-selected','--border','--text','--text-muted','--text-dim','--accent','--accent-hover','--accent-contrast','--user-bg','--assistant-bg','--tool-bg','--bg-subtle','--danger','--success','--thinking-active','--shadow-color'];
 for(const id of darkThemes){
  const body=block(id);
  if(!body){fail(`夜间主题 "${id}" 在 tokens.css 里没有块（运行时整页回退亮色）`);continue;}
  const keys=new Set(body.split(';').map(part=>part.split(':')[0].trim()).filter(Boolean));
  const missing=required.filter(k=>!keys.has(k));
  if(missing.length)fail(`夜间主题 "${id}" 缺少令牌：${missing.join(', ')}`);
  else ok(`夜间主题 "${id}" 完整（${required.length} 个令牌）`);
 }
 for(const id of lightThemes){
  if(id==='light')continue; // 默认调色板就是 :root
  if(!block(id))fail(`白天主题 "${id}" 在 tokens.css 里没有块`);
 }
 ok(`主题清单与 CSS 对齐（白天 ${lightThemes.join('/')}；夜间 ${darkThemes.join('/')}）`);
 const code=readFileSync(resolve(root,'src/styles/code.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 for(const id of darkThemes){
  if(!new RegExp(`\\[data-theme="${id}"\\]`).test(code))fail(`code.css 未覆盖夜间主题 "${id}"（代码块会回退浅色配色）`);
 }
 const darkSwitch=code.includes('prefers-color-scheme');
 if(darkSwitch)fail('code.css 又出现了 prefers-color-scheme 深色块');
}
// 主题色值只能出现在 tokens.css / code.css：app.css 里写死颜色会让
// 「跟随系统」与显式主题在某些组件上不同步。
{
 const css=readFileSync(resolve(root,'src/styles/app.css'),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
 const has=(re)=>[...css.matchAll(re)].map(match=>match[0]);
 // 允许的例外：叠加层/阴影用的黑色透明白（与主题无关），以及 xterm 终端底色。
 const allowed=/^#000[0-9a-f]?$|^rgba\(0,0,0,\.[0-9]+\)$/i;
 const offenders=has(/#[0-9a-f]{6,8}\b/gi).filter(value=>!allowed.test(value));
 if(offenders.length)fail(`app.css 里出现了具体主题色值（应放进 tokens.css）：${[...new Set(offenders)].join(' ')}`);
 else ok('app.css 未内联主题色值');
}
// 代码配色表必须与高亮模块一起按需加载，不能进首屏：
// 它只在有代码块时才需要，而首屏预算已经很紧。
{
 const entry=readFileSync(resolve(root,'src/entry/app.ts'),'utf8');
 if(/styles\/code\.css/.test(entry))fail('code.css 被入口引入，会进首屏；应由 hljs 模块按需引入');
 else ok('code.css 未进首屏');
 const hljs=readFileSync(resolve(root,'src/lib/hljs.ts'),'utf8');
 if(!/styles\/code\.css/.test(hljs))fail('hljs 模块未引入 code.css，代码块会没有配色');
 if(/highlight\.js\/styles/.test(hljs))fail('hljs 模块仍引入 highlight.js 自带主题（浅色主题下会成为深色方块）');
}
// 高亮配色必须由令牌驱动，且 .hljs 自身不得画背景：
// 厂商主题给 <code> 铺的底色会在浅色 <pre> 上形成「浅框套深块」。
{
 const css=readFileSync(resolve(root,'src/styles/code.css'),'utf8');
 if(!/\.hljs\s*\{[^}]*background\s*:\s*transparent/.test(css))fail('code.css 必须清掉 .hljs 自身的背景');
 const hardcoded=[...css.matchAll(/color\s*:\s*#[0-9a-f]{3,8}/gi)].map(match=>match[0]);
 if(hardcoded.length)fail(`code.css 里出现硬编码颜色，应改为 var(--code-*)：${hardcoded.slice(0,3).join(' ')}`);
 else ok('高亮配色由令牌驱动');
}
const seen=new Set();
for(const group of ['fireAndForget','needsResponse','unsupportedByRpc']){
 const methods=manifest.extensionChannel?.[group];
 if(!Array.isArray(methods)){fail(`缺少扩展方法集合 ${group}`);continue;}
 for(const method of methods){if(seen.has(method))fail(`扩展方法分类重复: ${method}`);seen.add(method);}
}
const output=resolve(root,manifest.build.outputDir);
const manifestPath=resolve(root,manifest.build.viteManifest);
if(!existsSync(manifestPath)){fail('构建产物不存在，请先运行 pnpm build');}
else {
 const chunks=JSON.parse(readFileSync(manifestPath,'utf8'));
 const entry=manifest.build.entry??'src/entry/app.ts';
 const visited=new Set();const files=new Set();
 function visit(key){
  if(visited.has(key))return;visited.add(key);
  const chunk=chunks[key];if(!chunk){fail(`缺少静态依赖 ${key}`);return;}
  files.add(chunk.file);for(const css of chunk.css??[])files.add(css);
  for(const dependency of chunk.imports??[])visit(dependency);
 }
 visit(entry);
 let bytes=0;let ownBytes=0;let vendorBytes=0;
 // 供应商分块由 vite.config.ts 的 manualChunks 显式命名（htmx），
 // 也可能来自 manifest 的 src 字段。分开统计的目的：预算里有一块
 // 是「换不掉的第三方库」，把它和自己的代码混在一起，等于每次改 UI
 // 都在和别人的体积抢额度。
 const vendorNames=manifest.build.vendorChunks??[];
 const isVendor=(relative,chunk)=>Boolean(chunk&&((chunk.src??'').startsWith('node_modules/')||vendorNames.some((name)=>relative.includes(`/${name}-`)||relative.endsWith(`/${name}.js`))));
 const chunkOf=new Map();
 for(const [,chunk] of Object.entries(chunks)){if(chunk.file)chunkOf.set(chunk.file,chunk);for(const css of chunk.css??[])chunkOf.set(css,chunk);}
 for(const relative of files){
  const file=resolve(output,relative);
  if(!file.startsWith(output+sep)||!existsSync(file)){fail(`产物路径无效: ${relative}`);continue;}
  const data=readFileSync(file);const size=gzipSync(data,{level:9}).length;bytes+=size;
  if(isVendor(relative,chunkOf.get(relative)))vendorBytes+=size;else ownBytes+=size;
  if(/(?:katex|mermaid|cytoscape|xterm)/i.test(relative))fail(`重库进入首屏静态依赖: ${relative}`);
 }
 const budget=manifest.build.firstLoadBudgetGzipKB;
 if(!Number.isFinite(budget)||bytes>budget*1024)fail(`首屏 gzip ${(bytes/1024).toFixed(2)} KiB 超出总预算 ${budget} KiB（自有 ${(ownBytes/1024).toFixed(2)} + 供应商 ${(vendorBytes/1024).toFixed(2)}）`);
 else ok(`首屏 ${files.size} 个 JS/CSS 文件，gzip ${(bytes/1024).toFixed(2)} KiB / ${budget} KiB`);
 // 自有代码单独设限：这一块才是我们每次改动真正该盯住的数字。
 const ownBudget=manifest.build.firstLoadOwnBudgetGzipKB;
 if(!Number.isFinite(ownBudget))fail('manifest 缺少 build.firstLoadOwnBudgetGzipKB');
 else if(ownBytes>ownBudget*1024)fail(`首屏自有代码 gzip ${(ownBytes/1024).toFixed(2)} KiB 超出预算 ${ownBudget} KiB`);
 else ok(`首屏自有代码 gzip ${(ownBytes/1024).toFixed(2)} KiB / ${ownBudget} KiB（供应商 ${(vendorBytes/1024).toFixed(2)} KiB）`);
 // 所有动态分块也必须能在部署包中找到，不能靠裸包名绕过 Vite。
 for(const [key,chunk] of Object.entries(chunks)){
  for(const dependency of [...(chunk.imports??[]),...(chunk.dynamicImports??[])])if(!chunks[dependency])fail(`${key} 引用不存在的分块 ${dependency}`);
  for(const relative of [chunk.file,...(chunk.css??[]),...(chunk.assets??[])]){const file=resolve(output,relative);if(!file.startsWith(output+sep)||!existsSync(file))fail(`缺少分块产物 ${relative}`);}
 }
}
if(failed){console.error(`${failed} 项检查失败`);process.exit(1);}
console.log('UI 包契约与产物校验通过');
