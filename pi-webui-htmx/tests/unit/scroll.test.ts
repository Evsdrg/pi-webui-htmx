import { afterEach, expect, it, vi } from 'vitest';
import { isAtBottom, captureDistance, restoreDistance, captureAnchor, restoreAnchor, mountScroll } from '../../src/modules/scroll';

afterEach(() => { document.body.replaceChildren(); vi.unstubAllGlobals(); });
it('空容器与底部容差正确', () => { expect(isAtBottom({scrollHeight:0,scrollTop:0,clientHeight:500})).toBe(true); expect(isAtBottom({scrollHeight:2000,scrollTop:1460,clientHeight:500})).toBe(true); expect(isAtBottom({scrollHeight:2000,scrollTop:1400,clientHeight:500})).toBe(false); });
it('顶部插入内容时距离计算有明确上下界', () => { const old = {scrollHeight:2000,scrollTop:500,clientHeight:500}; expect(restoreDistance({scrollHeight:3000,scrollTop:500,clientHeight:500},captureDistance(old))).toBe(1500); expect(restoreDistance(old,3000)).toBe(0); expect(restoreDistance(old,-10)).toBe(1500); });
it('恢复可见回合的像素偏移而非盲目固定底部距离', () => {
  document.body.innerHTML = '<div id=scroll><article data-turn-id=a></article><article data-turn-id=b></article></div>';
  const scroller = document.getElementById('scroll')!; const [a,b] = Array.from(scroller.children) as HTMLElement[];
  vi.spyOn(scroller,'getBoundingClientRect').mockReturnValue({top:100} as DOMRect);
  vi.spyOn(a!,'getBoundingClientRect').mockReturnValue({top:-200,bottom:90} as DOMRect);
  const rect = vi.spyOn(b!,'getBoundingClientRect').mockReturnValue({top:90,bottom:400} as DOMRect);
  scroller.scrollTop = 500; const anchor = captureAnchor(scroller)!; expect(anchor).toEqual({id:'b',offset:-10});
  rect.mockReturnValue({top:390,bottom:700} as DOMRect);
  expect(restoreAnchor(scroller,anchor)).toBe(true); expect(scroller.scrollTop).toBe(800);
});
it('没有锚点时不凭空改变位置', () => { const scroller = document.createElement('div'); scroller.scrollTop = 120; expect(captureAnchor(scroller)).toBeNull(); expect(restoreAnchor(scroller,{id:'missing',offset:0})).toBe(false); expect(scroller.scrollTop).toBe(120); });

// 反例：桥因事件被省略而要求重同步时，会重读历史换入 #turns。htmx 换入的那一刻
// 正文的 markdown 还没同步渲染（app.ts 的 afterSwap 处理器注册在 mountScroll 之后、
// 同一次派发内更晚执行），布局是过渡态：#turns 更矮、锚点在流内偏上。若此时就按
// 锚点差额调整 scrollTop，等渲染完成布局长回去，这笔补偿就成了净位移；每轮重同步
// 叠加一次，用户看到视图被匀速拉向顶部（只有刷新才停）。还原必须在稳定布局上做。
//
// ResizeObserver 兜不住这一例：#turns 在同一帧里先矮后高、净变化为零，而 RO 只在
// 观察交付之间的净尺寸变化上报告，因此根本不触发。
it('换入历史按稳定布局还原，不在过渡布局上补偿', () => {
  document.body.innerHTML = '<button id="jump-latest"></button><div id="chat-scroll"><div id="welcome"></div><div id="older-slot"></div><div id="turns"><article data-turn-id="u1"></article></div><div id="live"></div></div>';
  const scroller = document.getElementById('chat-scroll') as HTMLElement;
  const turns = document.getElementById('turns') as HTMLElement;
  const u1 = document.querySelector<HTMLElement>('[data-turn-id="u1"]')!;
  Object.defineProperty(scroller, 'scrollHeight', { value: 5000, configurable: true });
  Object.defineProperty(scroller, 'clientHeight', { value: 500, configurable: true });
  let top = 1000;
  Object.defineProperty(scroller, 'scrollTop', { get: () => top, set: (value: number) => { top = value; }, configurable: true });
  // 'settled' = 正文渲染完成的稳定布局；'transient' = 刚换入、markdown 未渲染
  // （同一内容在过渡态更矮，锚点因此更靠上）。
  let phase: 'settled' | 'transient' = 'settled';
  vi.spyOn(scroller, 'getBoundingClientRect').mockReturnValue({ top: 0 } as DOMRect);
  vi.spyOn(u1, 'getBoundingClientRect').mockImplementation(() => (phase === 'transient' ? { top: -150, bottom: 50 } : { top: -100, bottom: 100 }) as DOMRect);
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  // 真实浏览器里 rAF 排在当前任务之后（即全部 afterSwap 处理器跑完之后、绘制之前）；
  // 这里排入手动队列，等模拟的 markdown 渲染完成（phase 回到 settled）再冲。
  const frames: FrameRequestCallback[] = [];
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.push(callback); return frames.length; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
  const scroll = mountScroll();
  const detail = () => ({ target: turns, xhr: { getResponseHeader: () => null }, shouldSwap: true });
  document.dispatchEvent(new CustomEvent('htmx:beforeSwap', { detail: detail() }));
  phase = 'transient';
  document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: detail() }));
  phase = 'settled';
  for (const callback of frames.splice(0)) callback(0);
  scroll.dispose();
  expect(top).toBe(1000);
});
