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
 let bytes=0;
 for(const relative of files){
  const file=resolve(output,relative);
  if(!file.startsWith(output+sep)||!existsSync(file)){fail(`产物路径无效: ${relative}`);continue;}
  const data=readFileSync(file);bytes+=gzipSync(data,{level:9}).length;
  if(/(?:katex|mermaid|cytoscape|xterm)/i.test(relative))fail(`重库进入首屏静态依赖: ${relative}`);
 }
 const budget=manifest.build.firstLoadBudgetGzipKB;
 if(!Number.isFinite(budget)||bytes>budget*1024)fail(`首屏 gzip ${(bytes/1024).toFixed(2)} KiB 超出预算 ${budget} KiB`);
 else ok(`首屏 ${files.size} 个 JS/CSS 文件，gzip ${(bytes/1024).toFixed(2)} KiB / ${budget} KiB`);
 // 所有动态分块也必须能在部署包中找到，不能靠裸包名绕过 Vite。
 for(const [key,chunk] of Object.entries(chunks)){
  for(const dependency of [...(chunk.imports??[]),...(chunk.dynamicImports??[])])if(!chunks[dependency])fail(`${key} 引用不存在的分块 ${dependency}`);
  for(const relative of [chunk.file,...(chunk.css??[]),...(chunk.assets??[])]){const file=resolve(output,relative);if(!file.startsWith(output+sep)||!existsSync(file))fail(`缺少分块产物 ${relative}`);}
 }
}
if(failed){console.error(`${failed} 项检查失败`);process.exit(1);}
console.log('UI 包契约与产物校验通过');
