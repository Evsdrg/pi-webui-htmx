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

// 截图里的「行距过大」不是 line-height 的问题：思考块的 markdown 渲染产物
// 被 .live .live-think 的 white-space:pre-wrap（特异性 0,2,0）压过了
// .thinking-md 的 normal（0,1,0），HTML 里每个换行符变成可见空行。
describe('思考块渲染后的空白处理', () => {
  it('markdown 容器的 white-space 必须是 normal，且不被更高特异性覆盖', () => {
    expect(declarations('.thinking-md')).toMatch(/white-space:\s*normal/);
    // pre-wrap 只允许留在纯文本路径上；带 markdown 的容器不能再命中它。
    const preWrap = rules
      .filter((m) => /white-space:\s*pre-wrap/.test(m[2]))
      .map((m) => m[1].trim());
    expect(preWrap.some((sel) => sel.includes('.live .live-think') && !sel.includes('.thinking-md'))).toBe(false);
  });
});
