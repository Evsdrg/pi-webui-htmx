import { afterEach, expect, it, vi } from 'vitest';
import { isAtBottom, captureDistance, restoreDistance, captureAnchor, restoreAnchor } from '../../src/modules/scroll';

afterEach(() => { document.body.replaceChildren(); });
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
