// 节点和计时器使用同一生命周期，插件刷屏也不会积累待执行任务。
export type NotifyKind = 'info' | 'success' | 'warning' | 'error';
const timers = new Map<HTMLElement, ReturnType<typeof setTimeout>>();
function remove(node: HTMLElement): void { const timer = timers.get(node); if (timer) clearTimeout(timer); timers.delete(node); node.remove(); }
export function clearToasts(): void { for (const node of timers.keys()) remove(node); document.getElementById('toast-root')?.replaceChildren(); }
export function showToast(message: string, kind: NotifyKind = 'info', ttlMs = 6000): void {
  if (!message.trim()) return;
  let shelf = document.getElementById('toast-root');
  if (!shelf) { shelf = document.createElement('div'); shelf.id = 'toast-root'; shelf.className = 'toast-root'; shelf.setAttribute('aria-live','polite'); document.body.append(shelf); }
  while (shelf.children.length >= 4) remove(shelf.firstElementChild as HTMLElement);
  const node = document.createElement('div'); node.className = `toast toast-${kind}`; node.textContent = message.slice(0, 500);
  const close = document.createElement('button'); close.className = 'icon-btn'; close.type = 'button'; close.setAttribute('aria-label','关闭通知'); close.textContent = '×'; close.addEventListener('click',() => remove(node),{once:true}); node.append(close); shelf.append(node);
  timers.set(node,setTimeout(() => remove(node),Math.max(1000,Math.min(30_000,ttlMs))));
}
