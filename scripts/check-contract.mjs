#!/usr/bin/env node
// 校验 UI 包是否满足 docs/contract.md 的要求。
// 桥在启动时做等价校验；这里是 UI 仓的自检，两边规则必须一致。
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const srcRoot = join(root, "src"); // manifest 的相对路径以 src/ 为基准
let failed = 0;

function fail(msg) {
  console.error(`✗ ${msg}`);
  failed++;
}
function ok(msg) {
  console.log(`✓ ${msg}`);
}

const manifestPath = join(root, "ui-manifest.json");
if (!existsSync(manifestPath)) {
  console.error("缺少 ui-manifest.json");
  process.exit(1);
}
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

for (const field of ["uiVersion", "protocolVersion", "piBaseline", "templates", "assets", "routes", "requiredMethods", "extensionChannel"]) {
  if (manifest[field] === undefined) fail(`manifest 缺少字段 ${field}`);
  else ok(`manifest.${field}`);
}

for (const [name, rel] of Object.entries(manifest.templates ?? {})) {
  if (name.startsWith("_")) continue; // 说明键，不是模板
  if (!existsSync(join(srcRoot, rel))) fail(`模板 ${name} -> ${rel} 不存在`);
  else ok(`模板 ${name}`);
}

for (const [name, rel] of Object.entries(manifest.assets ?? {})) {
  if (!existsSync(join(srcRoot, rel))) fail(`资源 ${name} -> ${rel} 不存在`);
  else ok(`资源 ${name}`);
}

// 契约 2.1：除 shell 外不得出现完整文档结构。
for (const [name, rel] of Object.entries(manifest.templates ?? {})) {
  if (name.startsWith("_")) continue;
  const p = join(srcRoot, rel);
  if (!existsSync(p)) continue;
  const body = readFileSync(p, "utf8");
  if (name === "shell") continue;
  // 用词边界匹配，避免把 class 名 diff-file-head 误判成 <head>。
  for (const [label, re] of [["<!DOCTYPE", /<!DOCTYPE/i], ["<html", /<html[\s>]/i], ["<head", /<head[\s>]/i], ["<body", /<body[\s>]/i]]) {
    if (re.test(body)) fail(`${name} 含 ${label}，片段模板不得输出完整文档`);
    else ok(`${name} 不含 ${label}`);
  }
  // 契约 2.1.3：不得内联 script。
  if (/<script/i.test(body)) fail(`${name} 内联了 <script>，违反 CSP 约束`);
  else ok(`${name} 无内联脚本`);
}

// 契约 2.1.4：不得用 style 承载动态值（允许静态 style 属性中的固定值）。
for (const [name, rel] of Object.entries(manifest.templates ?? {})) {
  if (name.startsWith("_")) continue;
  const p = join(srcRoot, rel);
  if (!existsSync(p)) continue;
  const body = readFileSync(p, "utf8");
  if (/style="[^"]*\{\{/.test(body)) fail(`${name} 的 style 属性含插值，应改用 class`);
  else ok(`${name} 的 style 无动态插值`);
}

// 契约 3.3：对话框模板必须带 data-dialog-id。
const dialogRel = manifest.templates?.extDialog;
if (dialogRel) {
  const p = join(srcRoot, dialogRel);
  if (existsSync(p)) {
    const body = readFileSync(p, "utf8");
    if (!body.includes("data-dialog-id")) fail("extDialog 缺少 data-dialog-id");
    else ok("extDialog 带 data-dialog-id");
    if (!body.includes("session.ui_response") && !body.includes("/ui-response")) {
      fail("extDialog 未接线 session.ui_response");
    } else ok("extDialog 已接线回执");
  }
}

// 契约 3.1：extensionChannel 三类集合必须齐备。
const ch = manifest.extensionChannel ?? {};
for (const key of ["fireAndForget", "needsResponse", "unsupportedByRpc"]) {
  if (!Array.isArray(ch[key])) fail(`extensionChannel.${key} 缺失或不是数组`);
  else ok(`extensionChannel.${key} (${ch[key].length})`);
}
const overlap = (ch.needsResponse ?? []).filter((m) => (ch.fireAndForget ?? []).includes(m));
if (overlap.length) fail(`两类方法重叠: ${overlap.join(", ")}`);
else ok("两类扩展方法无重叠");

if (failed) {
  console.error(`\n${failed} 项不满足契约`);
  process.exit(1);
}
console.log("\nUI 包契约校验通过");
