import { afterEach, describe, expect, it, vi } from 'vitest';
import { mountMermaid } from '@/modules/mermaid';

const { initialize, render, sanitize } = vi.hoisted(() => ({
  initialize: vi.fn(),
  render: vi.fn(async () => ({ svg: '<svg><text>测试图表</text></svg>' })),
  sanitize: vi.fn((svg: string) => svg),
}));
vi.mock('mermaid', () => ({ default: { initialize, render } }));
vi.mock('dompurify', () => ({ default: { sanitize } }));
afterEach(() => {
  document.body.replaceChildren();
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.mode;
  vi.clearAllMocks();
});

describe('图表按昼夜模式选择配色而不是只识别 dark 名称', () => {
  it.each(['dark', 'obsidian', 'jade', 'plum'])('%s 使用夜间图表配色', async (theme) => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.mode = 'dark';
    document.body.innerHTML = '<pre><code class="language-mermaid">graph LR; A--&gt;B</code></pre>';
    mountMermaid();
    await vi.waitFor(() => expect(document.querySelector('.mermaid')?.getAttribute('data-mermaid-rendered')).toBe('1'));
    expect(initialize).toHaveBeenCalledWith(expect.objectContaining({ theme: 'dark', securityLevel: 'strict' }));
    expect(sanitize).toHaveBeenCalled();
  });

  it('白天模式保持默认图表配色', async () => {
    document.documentElement.dataset.theme = 'pine';
    document.documentElement.dataset.mode = 'light';
    document.body.innerHTML = '<pre><code class="language-mermaid">graph LR; A--&gt;B</code></pre>';
    mountMermaid();
    await vi.waitFor(() => expect(document.querySelector('.mermaid')?.getAttribute('data-mermaid-rendered')).toBe('1'));
    expect(initialize).toHaveBeenCalledWith(expect.objectContaining({ theme: 'default' }));
  });
});
