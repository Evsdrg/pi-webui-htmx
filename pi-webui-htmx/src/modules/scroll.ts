// 翻页保持正在阅读的条目与像素偏移；追加内容只在原本贴底时跟随。
export interface ScrollMetrics { scrollHeight: number; scrollTop: number; clientHeight: number }
export function isAtBottom(el: ScrollMetrics, threshold = 48): boolean { return el.scrollHeight - el.scrollTop - el.clientHeight <= threshold; }
export function captureDistance(el: ScrollMetrics): number { return el.scrollHeight - el.scrollTop; }
export function restoreDistance(el: ScrollMetrics, distance: number): number { return Math.max(0, Math.min(el.scrollHeight - el.clientHeight, el.scrollHeight - distance)); }
export function scrollModeFrom(xhr: Pick<XMLHttpRequest, 'getResponseHeader'>): string | null { return xhr.getResponseHeader('X-Scroll-Mode'); }

export interface ViewportAnchor { id: string; offset: number }
export function captureAnchor(scroller: HTMLElement): ViewportAnchor | null {
  const top = scroller.getBoundingClientRect().top;
  for (const turn of scroller.querySelectorAll<HTMLElement>('[data-turn-id]')) {
    const rect = turn.getBoundingClientRect();
    if (rect.bottom > top + 1) return { id: turn.dataset.turnId ?? '', offset: rect.top - top };
  }
  return null;
}
export function restoreAnchor(scroller: HTMLElement, anchor: ViewportAnchor): boolean {
  const node = Array.from(scroller.querySelectorAll<HTMLElement>('[data-turn-id]')).find((el) => el.dataset.turnId === anchor.id);
  if (!node) return false;
  scroller.scrollTop += node.getBoundingClientRect().top - scroller.getBoundingClientRect().top - anchor.offset;
  return true;
}

export function mountScroll(): { bottom(): void; dispose(): void } {
  const scroller = document.getElementById('chat-scroll');
  if (!scroller) return { bottom() {}, dispose() {} };
  const abort = new AbortController();
  let pinned = true;
  let anchor: ViewportAnchor | null = null;
  let pending: { atBottom: boolean; anchor: ViewportAnchor | null; distance: number; top: number; prepend: boolean; reset: boolean } | null = null;
  let frame = 0;
  let programmatic = false;
  const mark = () => {
    programmatic = true;
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => { programmatic = false; });
  };
  const updateButton = () => { const button = document.getElementById('jump-latest'); if (button) button.hidden = pinned; };
  const bottom = () => { mark(); scroller.scrollTop = scroller.scrollHeight; pinned = true; anchor = null; updateButton(); };
  scroller.addEventListener('scroll', () => {
    if (programmatic) return;
    pinned = isAtBottom(scroller);
    anchor = pinned ? null : captureAnchor(scroller);
    updateButton();
  }, { passive: true, signal: abort.signal });
  scroller.addEventListener('wheel', () => { anchor = null; }, { passive: true, signal: abort.signal });
  scroller.addEventListener('touchstart', () => { anchor = null; }, { passive: true, signal: abort.signal });
  document.addEventListener('htmx:beforeSwap', (event) => {
    const detail = (event as CustomEvent).detail as { target?: HTMLElement; xhr?: XMLHttpRequest; shouldSwap?: boolean };
    if (detail.target?.id !== 'turns' || detail.shouldSwap === false) return;
    pending = { atBottom: isAtBottom(scroller), anchor: captureAnchor(scroller), distance: captureDistance(scroller), top: scroller.scrollTop, prepend: detail.xhr?.getResponseHeader('X-Scroll-Mode') === 'prepend', reset: scroller.dataset.resetScroll === 'true' };
  }, { signal: abort.signal });
  document.addEventListener('htmx:afterSwap', (event) => {
    const target = (event as CustomEvent).detail?.target as HTMLElement | undefined;
    if (target?.id !== 'turns' || !pending) return;
    const saved = pending; pending = null; delete scroller.dataset.resetScroll;
    if (saved.reset || (saved.atBottom && !saved.prepend)) { bottom(); return; }
    mark();
    anchor = saved.anchor;
    if (!anchor || !restoreAnchor(scroller, anchor)) scroller.scrollTop = saved.prepend ? restoreDistance(scroller, saved.distance) : saved.top;
    pinned = isAtBottom(scroller); updateButton();
  }, { signal: abort.signal });
  // Markdown、图片和流式文本异步增高时也保持阅读位置。
  const observer = new ResizeObserver(() => {
    if (pending) return;
    if (pinned) bottom();
    else if (anchor) { mark(); restoreAnchor(scroller, anchor); }
  });
  for (const id of ['turns', 'live']) { const el = document.getElementById(id); if (el) observer.observe(el); }
  return { bottom, dispose() { abort.abort(); observer.disconnect(); cancelAnimationFrame(frame); } };
}
