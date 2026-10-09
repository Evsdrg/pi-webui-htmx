#!/usr/bin/env node
// 文字、控件状态和代码高亮均按 WCAG AA 正文标准核算；主题清单来自运行时模块。
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const read = path => readFileSync(resolve(root, path), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const css = read('src/styles/tokens.css');
const codeCSS = read('src/styles/code.css');
const themeSource = read('src/modules/theme.ts');
const failures = [];
function themeList(name) {
  const body = new RegExp(`${name}\\s*=\\s*\\[([^\\]]+)\\]`).exec(themeSource)?.[1];
  const ids = [...(body ?? '').matchAll(/'([a-z-]+)'/g)].map(match => match[1]);
  if (!ids.length) failures.push(`主题清单 ${name} 为空`);
  return ids;
}
const names = [...themeList('LIGHT_THEMES'), ...themeList('DARK_THEMES')];

// :root 可分段，代码配色可共用选择器；两者都按 CSS 源码顺序合并。
function tokens(source, selector) {
  const merged = {};
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (!match[1].split(',').some(value => value.trim() === selector)) continue;
    for (const declaration of match[2].matchAll(/(--[\w-]+)\s*:\s*([^;]+);?/g)) merged[declaration[1]] = declaration[2].trim();
  }
  return merged;
}
const base = tokens(css, ':root');
const codeBase = tokens(codeCSS, ':root');
const channel = value => { const c = value / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
const luminance = value => {
  const [r, g, b] = [1, 3, 5].map(offset => parseInt(value.slice(offset, offset + 2), 16));
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};
const contrast = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };
let pairs = 0;
function check(name, palette, foreground, background) {
  const fg = palette[foreground], bg = palette[background];
  if (![fg, bg].every(value => /^#[0-9a-f]{6}$/i.test(value ?? ''))) {
    failures.push(`${name} 的 ${foreground} 或 ${background} 缺少六位十六进制色值`);
    return;
  }
  pairs++;
  const ratio = contrast(fg, bg);
  if (ratio < 4.5) failures.push(`${name}: ${foreground} (${fg}) 在 ${background} (${bg}) 上只有 ${ratio.toFixed(2)}:1`);
}
for (const name of names) {
  const selector = `[data-theme="${name}"]`;
  const override = tokens(css, selector);
  if (name !== 'light' && !Object.keys(override).length) failures.push(`主题 ${name} 缺少调色板`);
  const palette = { ...base, ...override, ...codeBase, ...tokens(codeCSS, selector) };
  for (const surface of ['--bg', '--bg-panel', '--bg-hover', '--bg-selected', '--user-bg', '--tool-bg']) {
    for (const foreground of ['--text', '--text-muted', '--text-dim']) check(name, palette, foreground, surface);
  }
  for (const surface of ['--bg', '--bg-panel']) {
    for (const foreground of ['--accent', '--danger', '--success']) check(name, palette, foreground, surface);
  }
  for (const surface of ['--accent', '--accent-hover', '--success', '--text-dim']) check(name, palette, '--accent-contrast', surface);
  for (const foreground of Object.keys(codeBase).filter(key => key.startsWith('--code-'))) check(name, palette, foreground, '--tool-bg');
}
if (failures.length) { for (const line of failures) console.error(`✗ ${line}`); process.exit(1); }
console.log(`✓ 主题对比度：${names.length} 个主题、${pairs} 对文字/背景组合（含悬停、选中、按钮与代码），全部 ≥4.5:1`);
