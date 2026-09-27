import { describe, expect, it, vi } from 'vitest';
import { mount } from '../helpers/dom';

// 构造一棵最小会话树：根 → u1 → a1；u1 另有一个分支 a1b。
const tree = {
  tree: [
    { entry: { type: 'message', id: 'u1', message: { role: 'user', content: '第一个问题' } }, children: [
      { entry: { type: 'message', id: 'a1', message: { role: 'assistant', content: '回答一' } }, children: [] },
      { entry: { type: 'message', id: 'a1b', message: { role: 'assistant', content: '分支回答' } }, children: [] },
    ] },
  ],
  leafId: 'a1',
};
const forks = { messages: [{ entryId: 'u1', text: '第一个问题' }] };

describe('分支导航', () => {
  it('渲染树并标注分叉点与当前叶子', async () => {
    const { document, cleanup } = mount(`
      <dialog id=branch-dialog></dialog><p id=branch-status></p>
      <section id=branch-tree></section><section id=branch-forks></section>`);
    const requests: string[] = [];
    const bridge = {
      request: vi.fn(async (method: string, sessionId: string) => { requests.push(method + '@' + sessionId); return method === 'session.tree' ? tree : forks; }),
      sessionId: 's1',
    } as never;
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator(bridge, () => {}, () => {}, () => {}, () => 's1');
    await nav.open();
    // 必须带会话 ID：桥对 session.* 命令要求它，空串会被当成未启动拒绝。
    expect(requests).toEqual(['session.tree@s1', 'session.fork_messages@s1']);
    const rows = document.querySelectorAll('.branch-node');
    expect(rows).toHaveLength(3);
    // 分叉点标注子节点数。
    expect(rows[0].querySelector('.branch-kind')!.textContent).toContain('⑂2');
    // 当前叶子标 aria-current 且文案为「当前」。
    const current = document.querySelector('.branch-leaf[aria-current="true"]');
    expect(current).not.toBeNull();
    expect(current!.textContent).toBe('当前');
    expect(document.getElementById('branch-status')!.textContent).toBe('');
    cleanup();
  });

  it('点击叶子把 leafId 交给跳转回调', async () => {
    const { document, cleanup } = mount(`
      <dialog id=branch-dialog></dialog><p id=branch-status></p>
      <section id=branch-tree></section><section id=branch-forks></section>`);
    const bridge = { request: vi.fn(async (m: string) => (m === 'session.tree' ? tree : forks)), sessionId: 's1' } as never;
    const goto = vi.fn();
    const { BranchNavigator } = await import('@/modules/branch');
    await new BranchNavigator(bridge, () => {}, goto, () => {}, () => 's1').open();
    // 第二个叶子（分支回答）不是当前叶子，点它应跳转。
    const leaves = document.querySelectorAll<HTMLButtonElement>('.branch-leaf');
    leaves[2].click();
    expect(goto).toHaveBeenCalledWith('a1b');
    cleanup();
  });

  it('同时接受 {messages:[]} 与裸数组两种 fork 形状', async () => {
    const wrapped = { messages: [{ entryId: 'u1', text: '第一个问题' }] };
    const bare = [{ entryId: 'u1', text: '第一个问题' }];
    for (const payload of [wrapped, bare]) {
      const { document, cleanup } = mount(`<dialog id=branch-dialog></dialog><p id=branch-status></p><section id=branch-tree></section><section id=branch-forks></section>`);
      const bridge = { request: vi.fn(async (m: string) => (m === 'session.tree' ? { tree: [], leafId: null } : payload)) } as never;
      const { BranchNavigator } = await import('@/modules/branch');
      await new BranchNavigator(bridge, () => {}, () => {}, () => {}, () => 's1').open();
      expect(document.querySelectorAll('.branch-fork-row')).toHaveLength(1);
      cleanup();
    }
  });

  it('空树与空 fork 列表都有可读提示', async () => {
    const { document, cleanup } = mount(`
      <dialog id=branch-dialog></dialog><p id=branch-status></p>
      <section id=branch-tree></section><section id=branch-forks></section>`);
    const bridge = { request: vi.fn(async () => ({ tree: [], leafId: null, messages: [] })) } as never;
    const { BranchNavigator } = await import('@/modules/branch');
    await new BranchNavigator(bridge, () => {}, () => {}, () => {}, () => 's1').open();
    expect(document.querySelector('#branch-tree')!.textContent).toContain('还没有分支结构');
    expect(document.querySelector('#branch-forks')!.textContent).toContain('没有可分支的用户消息');
    cleanup();
  });

  it('读取失败时在状态行显示原因，不抛异常', async () => {
    const { document, cleanup } = mount(`
      <dialog id=branch-dialog></dialog><p id=branch-status></p>
      <section id=branch-tree></section><section id=branch-forks></section>`);
    const onError = vi.fn();
    const bridge = { request: vi.fn(async () => { throw new Error('Pi 拒绝了 get_tree'); }) } as never;
    const { BranchNavigator } = await import('@/modules/branch');
    await new BranchNavigator(bridge, onError, () => {}, () => {}, () => 's1').open();
    expect(onError).toHaveBeenCalled();
    expect(document.getElementById('branch-status')!.textContent).toContain('Pi 拒绝了');
    cleanup();
  });
});

describe('深树不爆栈', () => {
  // U08：flatten 以前用递归，长线性会话的分支树会在摊平阶段就抛
  // Maximum call stack size exceeded，整个分支面板打不开。
  // 直接测 flatten：绕开 DOM，才能把深度推到可靠溢出的量级。
  it('20 万层线性树可摊平且结果完整', async () => {
    const { flatten } = await import('@/modules/branch');
    let node: Record<string, unknown> = { entry: { type: 'message', id: 'leaf', message: { role: 'assistant', content: '末端' } }, children: [] };
    for (let i = 0; i < 200_000; i++) {
      node = { entry: { type: 'message', id: `n${i}`, message: { role: 'user', content: 'x' } }, children: [node] };
    }
    const rows = flatten([node as never]);
    // 200001 = 200000 层 + 末端叶子。
    expect(rows).toHaveLength(200_001);
    // 深度必须逐层递增，且根为 0。
    expect(rows[0]?.depth).toBe(0);
    expect(rows[200_000]?.depth).toBe(200_000);
  });

  it('多根树保持前序', async () => {
    const { flatten } = await import('@/modules/branch');
    const leaf = (id: string): Record<string, unknown> => ({ entry: { type: 'message', id, message: { role: 'user', content: id } }, children: [] });
    const rows = flatten([leaf('a') as never, leaf('b') as never]);
    expect(rows.map((r) => (r.node as { entry: { id: string } }).entry.id)).toEqual(['a', 'b']);
    expect(rows.every((r) => r.depth === 0)).toBe(true);
  });
});
