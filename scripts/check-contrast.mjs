#!/usr/bin/env node
// 主题对比度核算：把「文字色 / 语义色 vs 背景与面板」逐对算一遍，
// 低于 WCAG AA 正文标准（4.5:1）就报错。
//
// 为什么要有这个脚本：色值改动看起来无害，但一个令牌被两种底色复用、
// 或某主题少压深一档，就会静默掉到 4.5 以下（mist 的 --text-dim 曾只有
// 4.47，pine 的 --accent 只有 4.45）。axe 在浏览器里只能覆盖它测量到的
// 元素，这里覆盖的是令牌本身，与页面结构无关。
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const css = readFileSync(resolve(root, 'src/styles/tokens.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

// 同一选择器可能出现多次（:root 分段定义了派生令牌、浅色调色板、别名），
// 因此按出现顺序全部合并，后出现的覆盖先出现的。
function tokens(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const pattern = new RegExp(`(?:^|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`, 'gm');
  const merged = {};
  for (const match of css.matchAll(pattern)) {
    for (const [key, value] of match[1].split(';').map(part => part.split(':')).filter(pair => pair.length === 2)) {
      merged[key.trim()] = value.trim();
    }
  }
  return merged;
}

// 深色主题的值指向 --dark-* 别名，这里展开成字面色值。
const base = tokens(':root');
function hex(value) {
  const resolved = value.replace(/var\((--[\w-]+)\)/, (_, name) => base[name] ?? '');
  return resolved.startsWith('#') ? resolved : null;
}

const themes = {
  light: { ...base },
  dark: { ...base, ...tokens('[data-theme="dark"]') },
  mist: { ...base, ...tokens('[data-theme="mist"]') },
  rose: { ...base, ...tokens('[data-theme="rose"]') },
  pine: { ...base, ...tokens('[data-theme="pine"]') },
};

const channel = value => { const c = value / 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
const luminance = value => {
  const hex6 = value.slice(1, 7);
  const [r, g, b] = [0, 2, 4].map(offset => parseInt(hex6.slice(offset, offset + 2), 16));
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};
const contrast = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };

// 这些令牌会被当作正文级文字色使用（含 10–11px 的小字）。
const foregrounds = ['--text', '--text-muted', '--text-dim', '--accent', '--danger', '--success'];
const failures = [];
for (const [name, palette] of Object.entries(themes)) {
  for (const surface of ['--bg', '--bg-panel']) {
    if (!palette[surface]) { failures.push(`${name} 缺少 ${surface}`); continue; }
    for (const foreground of foregrounds) {
      const fg = hex(palette[foreground] ?? ''), bg = hex(palette[surface]);
      if (!fg || !bg) { failures.push(`${name} 的 ${foreground} 或 ${surface} 不是字面色值`); continue; }
      const ratio = contrast(fg, bg);
      if (ratio < 4.5) failures.push(`${name}: ${foreground} (${fg}) 在 ${surface} (${bg}) 上只有 ${ratio.toFixed(2)}:1`);
    }
  }
}
if (failures.length) { for (const line of failures) console.error(`✗ ${line}`); process.exit(1); }
console.log(`✓ 主题对比度：${Object.keys(themes).length} 个主题 × ${foregrounds.length} 个文字色 × 2 种底色，全部 ≥4.5:1`);
