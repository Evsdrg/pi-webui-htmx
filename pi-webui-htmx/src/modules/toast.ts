// 节点、到期计时器与退场兜底共用生命周期，容量淘汰和清空不等待动画。
export type NotifyKind = 'info' | 'success' | 'warning' | 'error';
const EXIT_ANIMATION = 'toast-exit';
// 动画为 100ms；样式缺失或动画事件未派发时仍须释放节点。
const EXIT_FALLBACK_MS = 150;
const timers = new Map<HTMLElement, ReturnType<typeof setTimeout>>();
const exits = new Map<HTMLElement, ReturnType<typeof setTimeout>>();
const handlers = new WeakMap<HTMLElement, (event: Event) => void>();
function prefersReducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches === true;
}
function remove(node: HTMLElement): void {
  const ttl = timers.get(node); if (ttl !== undefined) clearTimeout(ttl); timers.delete(node);
  const exit = exits.get(node); if (exit !== undefined) clearTimeout(exit); exits.delete(node);
  const handler = handlers.get(node); if (handler) node.removeEventListener('animationend', handler); handlers.delete(node);
  node.remove();
}
function beginExit(node: HTMLElement): void {
  if (node.hasAttribute('data-leaving') || exits.has(node)) return;
  const ttl = timers.get(node); if (ttl !== undefined) clearTimeout(ttl); timers.delete(node);
  if (prefersReducedMotion()) { remove(node); return; }
  node.setAttribute('data-leaving', ''); node.setAttribute('inert', '');
  const handler = (event: Event) => {
    if (event.target !== node || (event as AnimationEvent).animationName !== EXIT_ANIMATION) return;
    remove(node);
  };
  handlers.set(node, handler); node.addEventListener('animationend', handler);
  exits.set(node, setTimeout(() => remove(node), EXIT_FALLBACK_MS));
}
export function clearToasts(): void {
  for (const node of [...timers.keys()]) remove(node);
  for (const node of [...exits.keys()]) remove(node);
  document.getElementById('toast-root')?.replaceChildren();
}
export function showToast(message: string, kind: NotifyKind = 'info', ttlMs = 6000): void {
  if (!message.trim()) return;
  let shelf = document.getElementById('toast-root');
  if (!shelf) { shelf = document.createElement('div'); shelf.id = 'toast-root'; shelf.className = 'toast-root'; shelf.setAttribute('aria-live','polite'); document.body.append(shelf); }
  while (shelf.children.length >= 4) remove(shelf.firstElementChild as HTMLElement);
  const node = document.createElement('div'); node.className = `toast toast-${kind}`; node.textContent = message.slice(0, 500);
  const close = document.createElement('button'); close.className = 'icon-btn'; close.type = 'button'; close.setAttribute('aria-label','关闭通知'); close.textContent = '×'; close.addEventListener('click',() => beginExit(node),{once:true}); node.append(close); shelf.append(node);
  timers.set(node,setTimeout(() => beginExit(node),Math.max(1000,Math.min(30_000,ttlMs))));
}
