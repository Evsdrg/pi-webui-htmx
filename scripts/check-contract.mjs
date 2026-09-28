#!/usr/bin/env node
// 检查模板结构与生产产物；交互接线由 tests/unit 和浏览器验收负责。
import { readFileSync, existsSync } from 'node:fs';
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
 if(name!=='shell'&&/<!doctype|<(?:html|head|body)[\s>]/i.test(body))fail(`片段 ${name} 包含完整文档结构`);
 if(name!=='shell'&&/<script[\s>]/i.test(body))fail(`片段 ${name} 含脚本`);
 if(/style="[^"]*\{\{/.test(body))fail(`模板 ${name} 含动态内联样式`);
 if(name==='extDialog'&&(!body.includes('data-dialog-id')||!body.includes('data-extension-form')))fail('扩展对话缺少回执表单标记');
 ok(`模板 ${name}`);
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
