import { describe, expect, it, vi } from 'vitest';
import { LiveView } from '@/modules/stream';

function mount(): HTMLElement {
  const root = document.createElement('div');
  root.innerHTML = '<div id=live-thinking></div><div id=live-text></div><div id=live-tools></div><div id=live-user></div>';
  document.body.append(root);
  return root;
}

describe('实时思考增量', () => {
  // U21：旧实现每个 delta 都「读整段 textContent + 拼 + 写回」，
  // 最多 40K 字符被反复复制。改为增量追加文本节点。
  it('增量追加文本节点，不做整段重写', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    const el = root.querySelector('#live-thinking')!;
    const appendSpy = vi.spyOn(el, 'append');
    const setter = vi.spyOn(el, 'textContent', 'set');

    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '甲' } });
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '乙' } });

    expect(appendSpy).toHaveBeenCalledTimes(2);
    // 关键：不得再整段写回 textContent。
    expect(setter).not.toHaveBeenCalled();
    expect(el.textContent).toBe('甲乙');
    root.remove();
  });

  it('超过上限后停止追加', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    const big = 'x'.repeat(1000);
    for (let i = 0; i < 60; i++) view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: big } });
    const el = root.querySelector('#live-thinking')!;
    expect((el.textContent ?? '').length).toBe(40_000);
    root.remove();
  });

  it('begin 之后重新计数', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: 'y'.repeat(1000) } });
    view.begin();
    const el = root.querySelector('#live-thinking')!;
    expect(el.textContent).toBe('');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: 'z' } });
    expect(el.textContent).toBe('z');
    root.remove();
  });
});
