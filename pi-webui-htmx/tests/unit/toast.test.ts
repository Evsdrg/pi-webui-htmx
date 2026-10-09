import { afterEach, expect, it, vi } from 'vitest';
import { showToast, clearToasts } from '../../src/modules/toast';

afterEach(() => { clearToasts(); vi.useRealTimers(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

// 派发一个可携带 animationName 的 animationend（jsdom 无 AnimationEvent 构造器）。
function animEnd(name: string, target: Element, bubbles = false): void {
  const event = new Event('animationend', { bubbles });
  Object.defineProperty(event, 'animationName', { value: name });
  target.dispatchEvent(event);
}

function stubReducedMotion(): void {
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('reduced-motion'), media: query, addEventListener() {}, removeEventListener() {} }));
}

it('关闭按钮先标记退场，动画结束才移除', () => {
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  node.querySelector<HTMLButtonElement>('button')!.click();
  expect(document.querySelector('.toast')).toBe(node); // 退场期间仍在 DOM
  expect(node.hasAttribute('data-leaving')).toBe(true);
  expect(node.hasAttribute('inert')).toBe(true);
  animEnd('toast-exit', node);
  expect(document.querySelector('.toast')).toBeNull();
});

it('过滤子节点冒泡的 animationend', () => {
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  const child = node.querySelector('button')!;
  child.click();
  animEnd('toast-exit', child, true); // 从子节点冒泡上来的同名事件不应移除自身
  expect(document.querySelector('.toast')).toBe(node);
  animEnd('toast-exit', node); // 自身该动画结束才移除
  expect(document.querySelector('.toast')).toBeNull();
});

it('过滤非 toast-exit 的动画名（如入场 toast-in）', () => {
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  node.querySelector<HTMLButtonElement>('button')!.click();
  animEnd('toast-in', node); // 其它动画名不得触发移除
  expect(document.querySelector('.toast')).toBe(node);
  animEnd('toast-exit', node);
  expect(document.querySelector('.toast')).toBeNull();
});

it('缺少动画事件时由有界兜底计时器移除', () => {
  vi.useFakeTimers();
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  node.querySelector<HTMLButtonElement>('button')!.click();
  expect(document.querySelector('.toast')).toBe(node);
  vi.advanceTimersByTime(149);
  expect(document.querySelector('.toast')).toBe(node);
  vi.advanceTimersByTime(1);
  expect(document.querySelector('.toast')).toBeNull();
  expect(vi.getTimerCount()).toBe(0);
});

it('TTL 到期后走退场流程，等待退场时间才移除', () => {
  vi.useFakeTimers();
  showToast('甲', 'info', 1000);
  const node = document.querySelector<HTMLElement>('.toast')!;
  vi.advanceTimersByTime(1000);
  expect(node.hasAttribute('data-leaving')).toBe(true);
  expect(document.querySelector('.toast')).toBe(node);
  vi.advanceTimersByTime(150);
  expect(document.querySelector('.toast')).toBeNull();
  expect(vi.getTimerCount()).toBe(0);
});

it('reduced-motion 下立即移除且不留计时器', () => {
  vi.useFakeTimers();
  stubReducedMotion();
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  node.querySelector<HTMLButtonElement>('button')!.click();
  expect(document.querySelector('.toast')).toBeNull();
  expect(vi.getTimerCount()).toBe(0);
});

it('重复关闭不重复计时，动画结束清掉兜底', () => {
  vi.useFakeTimers();
  showToast('甲');
  const node = document.querySelector<HTMLElement>('.toast')!;
  const close = node.querySelector<HTMLButtonElement>('button')!;
  close.click(); close.click();
  expect(vi.getTimerCount()).toBe(1); // 只保留一个退场兜底计时器
  animEnd('toast-exit', node);
  expect(document.querySelector('.toast')).toBeNull();
  expect(vi.getTimerCount()).toBe(0); // 动画结束顺手清掉兜底
});

it('clearToasts 立即清掉退场中的节点并取消兜底', () => {
  vi.useFakeTimers();
  showToast('甲'); showToast('乙');
  const nodes = [...document.querySelectorAll<HTMLElement>('.toast')];
  nodes[0].querySelector<HTMLButtonElement>('button')!.click();
  expect(nodes[0].hasAttribute('data-leaving')).toBe(true);
  clearToasts();
  expect(document.querySelectorAll('.toast')).toHaveLength(0);
  expect(vi.getTimerCount()).toBe(0);
  vi.advanceTimersByTime(200); // 兜底推进也不应让节点复活
  expect(document.querySelectorAll('.toast')).toHaveLength(0);
});

it('容量淘汰立即移除，不进入退场阻塞', () => {
  for (let i = 0; i < 4; i++) showToast(`第${i}条`);
  const first = document.querySelector<HTMLElement>('.toast')!;
  showToast('第五条');
  expect(document.querySelectorAll('.toast')).toHaveLength(4);
  expect(first.isConnected).toBe(false);
  expect(document.querySelector('.toast')!.textContent).not.toContain('第0条');
});
