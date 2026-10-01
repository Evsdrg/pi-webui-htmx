import { describe, expect, it, vi } from 'vitest';
import { mount } from '../helpers/dom';

// 分支树现在由桥渲染（Go 侧 internal/presentation 的 BranchRows 有对应
// 单测，含 20 万层深树不爆栈的反例）。这个文件只测留在浏览器里的那一层：
// 带会话 ID 触发片段、把点击翻译成 goto/fork、把失败写进状态行。
describe('分支导航', () => {
  function setup() {
    const { document, cleanup } = mount(`
      <dialog id=branch-dialog></dialog><p id=branch-status></p>
      <input id=branch-session type=hidden>
      <div id=branch-body></div>`);
    const triggers: string[] = [];
    (window as unknown as { htmx: unknown }).htmx = {
      trigger: (_el: Element, name: string) => triggers.push(name),
      ajax: vi.fn(async () => {}),
    };
    return { document, cleanup, triggers };
  }

  it('打开时带会话 ID 触发一次片段刷新', async () => {
    const { document, cleanup, triggers } = setup();
    const bridge = {} as never;
    const { BranchNavigator } = await import('@/modules/branch');
    await new BranchNavigator(bridge, () => {}, () => {}, () => {}, () => 's1').open();
    // 会话 ID 必须写进 hx-include 的隐藏输入：桥对 session.* 要求它，
    // 空串会被当成「未启动」拒绝。
    expect(document.getElementById('branch-session')!.value).toBe('s1');
    expect(triggers).toEqual(['branch-refresh']);
    cleanup();
  });

  it('点击叶子把 leafId 交给跳转回调', async () => {
    const { document, cleanup } = setup();
    const goto = vi.fn();
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator({} as never, () => {}, goto, () => {}, () => 's1');
    document.getElementById('branch-body')!.innerHTML =
      '<button class=branch-leaf data-branch-goto="a1b">查看</button>';
    const button = document.querySelector<HTMLButtonElement>('[data-branch-goto]')!;
    button.click();
    // 点击走模块的委托；事件冒泡到 document，由 workbench 的 onClick 转进来。
    expect(nav.handleClick({ target: button, preventDefault() {} } as unknown as Event)).toBe(true);
    expect(goto).toHaveBeenCalledWith('a1b');
    cleanup();
  });

  it('点击「分支」把 entryId 交给 fork 回调', async () => {
    const { document, cleanup } = setup();
    const fork = vi.fn();
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator({} as never, () => {}, () => {}, fork, () => 's1');
    document.getElementById('branch-body')!.innerHTML =
      '<div class=branch-fork-row><button data-branch-fork="u1">分支</button></div>';
    const button = document.querySelector<HTMLButtonElement>('[data-branch-fork]')!;
    nav.handleClick({ target: button, preventDefault() {} } as unknown as Event);
    expect(fork).toHaveBeenCalledWith('u1');
    cleanup();
  });

  it('面板外的点击不被吞掉', async () => {
    const { document, cleanup } = setup();
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator({} as never, () => {}, () => {}, () => {}, () => 's1');
    const outside = document.createElement('button');
    document.body.append(outside);
    expect(nav.handleClick({ target: outside, preventDefault() {} } as unknown as Event)).toBe(false);
    cleanup();
  });

  it('片段请求失败时把桥给的原因写进状态行', async () => {
    const { document, cleanup } = setup();
    const onError = vi.fn();
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator({} as never, onError, () => {}, () => {}, () => 's1');
    await nav.refresh();
    const xhr = {
      status: 409,
      responseURL: 'http://localhost/ui/branch?sessionId=s1',
      responseText: JSON.stringify({ error: { message: 'Pi 拒绝：会话未启动' } }),
    } as unknown as XMLHttpRequest;
    document.dispatchEvent(new CustomEvent('htmx:responseError', { detail: { xhr } }));
    expect(onError).toHaveBeenCalled();
    expect(document.getElementById('branch-status')!.textContent).toContain('会话未启动');
    cleanup();
  });

  it('提示留到片段落地；落在其它目标上不会误清', async () => {
    const { document, cleanup } = setup();
    const { BranchNavigator } = await import('@/modules/branch');
    const nav = new BranchNavigator({} as never, () => {}, () => {}, () => {}, () => 's1');
    await nav.refresh();
    expect(document.getElementById('branch-status')!.textContent).toBe('正在读取会话树…');
    // 别的片段（比如历史）交换不该清掉分支的提示。
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target: document.createElement('div') } }));
    expect(document.getElementById('branch-status')!.textContent).toBe('正在读取会话树…');
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target: document.getElementById('branch-body') } }));
    expect(document.getElementById('branch-status')!.textContent).toBe('');
    cleanup();
  });

  it('打开时先清掉上一次的残留提示', async () => {
    const { document, cleanup } = setup();
    document.getElementById('branch-status')!.textContent = '上一次的错误';
    const { BranchNavigator } = await import('@/modules/branch');
    await new BranchNavigator({} as never, () => {}, () => {}, () => {}, () => 's1').open();
    expect(document.getElementById('branch-status')!.textContent).toBe('正在读取会话树…');
    cleanup();
  });
});
