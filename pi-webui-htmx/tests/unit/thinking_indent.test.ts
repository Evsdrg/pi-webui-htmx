import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// 思考行收起时是「图标 + 思考 + 时长」的 flex 行；展开后若仍是 flex，
// 正文（flex:1）只能从标题右侧开始——实测正文左边缘 513px，而同列工具行在
// 396px，整段思考被推到右边 117px。展开态必须改回块级布局，让正文从行首起排。
const css = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
function declarations(selector: string): string {
  return rules.filter((m) => m[1].split(',').some((p) => p.trim() === selector)).map((m) => m[2]).join(';');
}

describe('思考块展开后的横向对齐', () => {
  it('展开态是块级布局，正文与同列工具行左对齐', () => {
    expect(declarations('.thinking-block[open]')).toMatch(/display:\s*block/);
    // 标题不再以 flex 项身份占据行首宽度。
    expect(declarations('.thinking-block[open] > summary')).not.toContain('flex:0 0 auto');
    // 块级化后正文不再需要 flex 拉伸。
    const body = declarations('.thinking-block .thinking-body');
    expect(body).not.toContain('flex:1 1 auto');
  });

  it('收起态仍是 flex 行（图标与文字同一行）', () => {
    expect(declarations('.thinking-block')).toMatch(/display:\s*flex/);
  });
});
