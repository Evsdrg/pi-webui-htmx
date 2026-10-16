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
    const saved = pending; delete scroller.dataset.resetScroll;
    // 还原不能在这次派发里做：htmx 换入 #turns 之后，正文的 markdown 同步渲染仍在
    // 同一次 afterSwap 派发中执行，只是注册顺序在本模块之后（见 entry/app.ts）。
    // 此刻量锚点差额，量到的是「原始文本尚未格式化」的过渡布局——#turns 更矮、
    // 锚点在流内偏上；渲染完成后布局复原，这笔补偿就成了净位移。反复重同步
    // （事件被省略 / resync_required）每次都叠一次，视图于是被匀速拉向顶部。
    // ResizeObserver 也兜不住：过渡态与稳定态在同一帧内先矮后高，净变化为零。
    // 推到下一帧：排在全部 afterSwap 处理器之后、绘制之前，布局已是终态。
    // pending 保留到那一刻，RO 才不会被过渡布局误导；若期间又换入一次，
    // pending 已被新的覆盖，这次回调据此让位（旧代次不落地）。
    requestAnimationFrame(() => {
      if (abort.signal.aborted || pending !== saved) return;
      pending = null;
      if (saved.reset || (saved.atBottom && !saved.prepend)) { bottom(); return; }
      mark();
      anchor = saved.anchor;
      if (!anchor || !restoreAnchor(scroller, anchor)) scroller.scrollTop = saved.prepend ? restoreDistance(scroller, saved.distance) : saved.top;
      pinned = isAtBottom(scroller); updateButton();
    });
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
