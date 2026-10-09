import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const css = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
function declarations(selector: string): string {
  return rules.filter((match) => match[1].split(',').some((part) => part.trim() === selector)).map((match) => match[2]).join(';');
}

describe('轻量动效的样式边界', () => {
  it('常用控件只过渡颜色，发送按钮可独立反馈按下状态', () => {
    for (const selector of ['.btn', '.icon-btn', '.session-item', '.view-btn', '.tool-item', '.copy-btn']) {
      expect(declarations(selector)).toMatch(/transition:[^;]*background-color/);
    }
    expect(declarations('#send-button:not(:disabled):active')).toContain('scale(.98)');
    expect(declarations('.composer-actions .select-label select:hover')).toBe('');
    expect(declarations('.composer-actions .select-label select:hover:not(:disabled)')).toContain('var(--bg-hover)');
    expect(css).not.toMatch(/transition:\s*all\b/);
  });

  it('会话操作隐藏时不可点击，聚焦和触屏仍可发现', () => {
    const ops = declarations('.session-ops');
    expect(ops).toContain('display:flex');
    expect(ops).toContain('opacity:0');
    expect(ops).toContain('visibility:hidden');
    expect(ops).toContain('pointer-events:none');
    expect(declarations('.session-item:focus-within .session-ops')).toContain('visibility:visible');
    expect(css).toMatch(/@media\s*\(hover:none\)\s*\{\s*\.session-ops\s*\{[^}]*visibility:visible/);
  });

  it('入场只挂菜单容器，通知有独立退场状态', () => {
    expect(declarations('.command-menu:not([hidden])')).toMatch(/animation:menu-in/);
    expect(declarations('.command-menu button')).not.toMatch(/animation:/);
    expect(declarations('.toast')).toMatch(/animation:toast-in/);
    expect(declarations('.toast[data-leaving]')).toMatch(/animation:toast-exit/);
    expect(declarations('.toast[data-leaving]')).toContain('pointer-events:none');
  });

  it('移动遮罩可离散退场，聊天内容不新增布局或入场动画', () => {
    expect(declarations('.sidebar-backdrop')).toContain('allow-discrete');
    expect(declarations('.sidebar-backdrop')).toContain('pointer-events:none');
    for (const selector of ['.turn', '.turn-process', '.process-body', '.streaming-text', '.chat-scroll']) {
      expect(declarations(selector)).not.toMatch(/(?:animation|transition|transform):/);
    }
    expect(declarations('.chat-scroll')).toContain('scrollbar-gutter:stable');
    expect(css).toMatch(/prefers-reduced-motion:reduce[\s\S]*animation:none !important[\s\S]*transition:none !important/);
  });
});
