import { describe, expect, it } from 'vitest';
import { swapRoots } from '@/lib/swap';

// 反例：htmx 在 outerHTML 交换后，detail.target 是旧节点（已脱离文档），
// 换入的新节点在 detail.elt。只认 target 会让换入的 .markdown 永远不渲染——
// 思考正文（按钮 → <div class="lazy-thinking markdown">）就是这条路径。
describe('htmx 交换根节点选择', () => {
  it('outerHTML：优先取 elt，同时保留 target', () => {
    const old = document.createElement('button');
    const fresh = document.createElement('div');
    const roots = swapRoots({ target: old, elt: fresh });
    expect(roots).toContain(fresh);
    expect(roots).toContain(old);
    expect(roots[0]).toBe(fresh);
  });

  it('innerHTML：只有 target', () => {
    const host = document.createElement('div');
    expect(swapRoots({ target: host })).toEqual([host]);
  });

  it('两者相同只返回一次', () => {
    const same = document.createElement('div');
    expect(swapRoots({ target: same, elt: same })).toEqual([same]);
  });

  it('缺失或非元素时返回空', () => {
    expect(swapRoots(undefined)).toEqual([]);
    expect(swapRoots({ target: 'x', elt: null })).toEqual([]);
  });
});
