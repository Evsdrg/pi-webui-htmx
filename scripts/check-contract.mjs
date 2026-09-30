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
  for(const mm of body.matchAll(/\b(hx-(?:get|post|put|delete)|action)="(\/[^"]*)"/g)){
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
// 深色主题有两处入口：「显式选深色」与「跟随系统」。两者必须给出完全相同的
// 令牌集合，否则在深色系统上看到的界面会和手动选深色不一致——这类分叉不会
// 报错，只会让某个主题路径静默变样（历史上 app.css 就重复写过一份深色值）。
{
 const extract=(css,pattern)=>{
  const match=new RegExp(`${pattern}\\s*\\{([^}]*)\\}`,'m').exec(css);
  if(!match)return null;
  return match[1].split(';').map(part=>part.split(':')[0].trim()).filter(Boolean).sort();
 };
 for(const file of ['src/styles/tokens.css','src/styles/code.css']){
  const css=readFileSync(resolve(root,file),'utf8').replace(/\/\*[\s\S]*?\*\//g,'');
  const explicit=extract(css,'\\[data-theme="dark"\\]');
  const system=extract(css,':root:not\\(\\[data-theme\\]\\)');
  if(!explicit||!system){fail(`${file} 缺少深色主题的某个入口（显式选择 / 跟随系统）`);continue;}
  if(JSON.stringify(explicit)!==JSON.stringify(system))fail(`${file} 的深色两处入口令牌不一致：显式 ${explicit.join(',')} / 系统 ${system.join(',')}`);
  else ok(`${file} 深色两处入口一致（${explicit.length} 个令牌）`);
 }
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
