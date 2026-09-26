#!/usr/bin/env node
// 校验 UI 包是否满足 docs/contract.md 的要求。
// 桥在启动时做等价校验；这里是 UI 仓的自检，两边规则必须一致。
import { readFileSync, existsSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const srcRoot = join(root, "src");
let failed = 0;

const fail = (msg) => { console.error(`✗ ${msg}`); failed++; };
const ok = (msg) => console.log(`✓ ${msg}`);

// ---- manifest 基本字段 ----
const manifest = JSON.parse(readFileSync(join(root, "ui-manifest.json"), "utf8"));
for (const field of ["uiVersion", "protocolVersion", "piBaseline", "templates", "routes", "requiredMethods", "extensionChannel", "build", "assets"]) {
  if (manifest[field] === undefined) fail(`manifest 缺少字段 ${field}`);
  else ok(`manifest.${field}`);
}

// ---- 模板存在且是片段 ----
for (const [name, rel] of Object.entries(manifest.templates ?? {})) {
  if (name.startsWith("_")) continue;
  const p = join(srcRoot, rel);
  if (!existsSync(p)) { fail(`模板 ${name} -> ${rel} 不存在`); continue; }
  ok(`模板 ${name}`);
  const body = readFileSync(p, "utf8");
  if (name === "shell") continue;
  for (const [label, re] of [["<!DOCTYPE", /<!DOCTYPE/i], ["<html", /<html[\s>]/i], ["<head", /<head[\s>]/i], ["<body", /<body[\s>]/i]]) {
    if (re.test(body)) fail(`${name} 含 ${label}，片段模板不得输出完整文档`);
  }
  if (/<script/i.test(body)) fail(`${name} 内联了 <script>，违反 CSP 约束`);
  if (/style="[^"]*\{\{/.test(body)) fail(`${name} 的 style 属性含插值，应改用 class`);
}

// ---- 扩展通道三集合 ----
const ch = manifest.extensionChannel ?? {};
for (const key of ["fireAndForget", "needsResponse", "unsupportedByRpc"]) {
  if (!Array.isArray(ch[key])) fail(`extensionChannel.${key} 缺失或不是数组`);
  else ok(`extensionChannel.${key} (${ch[key].length})`);
}
const overlap = (ch.needsResponse ?? []).filter((m) => (ch.fireAndForget ?? []).includes(m));
if (overlap.length) fail(`两类方法重叠: ${overlap.join(", ")}`);
else ok("两类扩展方法无重叠");

// ---- 对话框回执接线 ----
const dialogRel = manifest.templates?.extDialog;
if (dialogRel) {
  const p = join(srcRoot, dialogRel);
  if (existsSync(p)) {
    const body = readFileSync(p, "utf8");
    if (!body.includes("data-dialog-id")) fail("extDialog 缺少 data-dialog-id");
    else ok("extDialog 带 data-dialog-id");
    if (!body.includes("session.ui_response") && !body.includes("/ui-response")) fail("extDialog 未接线 session.ui_response");
    else ok("extDialog 已接线回执");
  }
}

// ---- 产物体积预算（仅当 dist/ 存在时检查）----
const budget = manifest.build?.firstLoadBudgetGzipKB ?? 32;
const viteManifest = join(root, manifest.build?.viteManifest ?? "dist/.vite/manifest.json");
if (existsSync(viteManifest)) {
  ok("构建产物存在，检查首屏预算");
  const vm = JSON.parse(readFileSync(viteManifest, "utf8"));
  const entry = Object.values(vm).find((v) => v.isEntry);
  if (!entry) {
    fail("Vite manifest 中找不到入口");
  } else {
    const files = [entry.file, ...(entry.css ?? [])].map((f) => join(root, manifest.build.outputDir, f));
    let total = 0;
    for (const f of files) {
      if (!existsSync(f)) { fail(`入口文件缺失: ${f}`); continue; }
      total += parseInt(execSync(`gzip -9c ${JSON.stringify(f)} | wc -c`).toString().trim(), 10);
    }
    const kb = Math.round(total / 1024);
    if (kb > budget) fail(`首屏 gzip ${kb} KB 超过预算 ${budget} KB`);
    else ok(`首屏 gzip ${kb} KB，预算 ${budget} KB`);
  }

  // 重库不得进入首屏 chunk。
  const entrySrc = readFileSync(join(root, manifest.build.outputDir, entry.file), "utf8");
  for (const lib of ["katex", "mermaid", "cytoscape", "xterm"]) {
    // 只查代码特征，不查 import 说明字符串。
    const patterns = { katex: "katex.renderToString", mermaid: "mermaid.render", cytoscape: "cytoscape(", xterm: "new Terminal" };
    if (entrySrc.includes(patterns[lib])) fail(`首屏包含 ${lib} 的实现代码，应拆为惰性 chunk`);
    else ok(`首屏不含 ${lib} 实现`);
  }
} else {
  console.log("· 未构建，跳过产物体积检查（pnpm vite build 后可用）");
}

if (failed) {
  console.error(`\n${failed} 项不满足契约`);
  process.exit(1);
}
console.log("\nUI 包契约校验通过");
