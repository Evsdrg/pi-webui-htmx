import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

let root: string;
const files = ['scripts/check-contrast.mjs', 'src/modules/theme.ts', 'src/styles/tokens.css', 'src/styles/code.css'];
beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), 'pi-theme-contrast-'));
  for (const file of files) {
    const target = join(root, file);
    mkdirSync(resolve(target, '..'), { recursive: true });
    writeFileSync(target, readFileSync(resolve(process.cwd(), file)));
  }
});
afterEach(() => { rmSync(root, { recursive: true, force: true }); });
function append(file: string, text: string): void {
  const target = join(root, file);
  writeFileSync(target, readFileSync(target, 'utf8') + text);
}
function failure(): string {
  const result = spawnSync(process.execPath, [join(root, 'scripts/check-contrast.mjs')], { encoding: 'utf8' });
  expect(result.status).toBe(1);
  return result.stderr;
}

describe('主题对比度检查不会漏掉交互底色与新主题', () => {
  it('八款主题的文字、按钮与代码配色通过', () => {
    expect(execFileSync(process.execPath, [join(root, 'scripts/check-contrast.mjs')], { encoding: 'utf8' })).toContain('8 个主题、304 对');
  });

  it('能拦住仅在选中态变得不可读的弱文字', () => {
    append('src/styles/tokens.css', '\n[data-theme="mist"] { --text-dim: #5c6a7d; }');
    expect(failure()).toContain('--bg-selected');
  });

  it('能拦住仅代码注释对比度不足的回归', () => {
    append('src/styles/code.css', '\n[data-theme="jade"] { --code-comment: #172720; }');
    expect(failure()).toContain('jade: --code-comment');
  });

  it('新增主题自动进入检查，缺少调色板不能继承浅色蒙混通过', () => {
    const file = join(root, 'src/modules/theme.ts');
    writeFileSync(file, readFileSync(file, 'utf8').replace("'dark', 'obsidian', 'jade', 'plum'", "'dark', 'obsidian', 'jade', 'plum', 'extra-night'"));
    expect(failure()).toContain('extra-night 缺少调色板');
  });
});
