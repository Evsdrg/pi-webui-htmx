// HTTP片段的请求归属。守卫在HX响应头和OOB处理之前执行。
// xhr保留发起时的快照，不能拿“最新请求的epoch”冒充旧xhr的epoch。
export function mountFragmentRequests(sessionEpoch: () => number, signal: AbortSignal): () => void {
  const scoped = new Set(['turns', 'older-slot', 'stats-body', 'system-body', 'tools-body', 'goal-body', 'branch-body', 'ext-dialog-slot', 'model-select', 'file-list', 'git-status', 'git-diff']);
  const active = new Map<HTMLElement, XMLHttpRequest>();
  const owners = new WeakMap<XMLHttpRequest, { target: HTMLElement; epoch: number; revision: string | undefined; sessionScoped: boolean }>();
  document.addEventListener('htmx:beforeRequest', (event) => {
    const { xhr, target } = (event as CustomEvent).detail ?? {};
    if (!xhr || !(target instanceof HTMLElement)) return;
    const previous = active.get(target);
    if (previous && previous !== xhr) previous.abort();
    active.set(target, xhr);
    owners.set(xhr, { target, epoch: sessionEpoch(), revision: target.dataset.requestScope, sessionScoped: scoped.has(target.id) || !!target.closest('#turns') });
  }, { signal });
  document.addEventListener('htmx:beforeOnLoad', (event) => {
    const xhr = (event as CustomEvent).detail?.xhr as XMLHttpRequest | undefined;
    const owner = xhr && owners.get(xhr);
    if (!owner) return;
    const { target, epoch, revision, sessionScoped } = owner;
    if (!target.isConnected || active.get(target) !== xhr || target.dataset.requestScope !== revision || (sessionScoped && epoch !== sessionEpoch())) event.preventDefault();
  }, { signal });
  document.addEventListener('htmx:afterRequest', (event) => {
    const xhr = (event as CustomEvent).detail?.xhr as XMLHttpRequest | undefined;
    const owner = xhr && owners.get(xhr);
    if (owner && active.get(owner.target) === xhr) active.delete(owner.target);
  }, { signal });
  signal.addEventListener('abort', () => { for (const xhr of active.values()) xhr.abort(); active.clear(); }, { once: true });
  return () => {
    for (const [target, xhr] of active) {
      if (!owners.get(xhr)?.sessionScoped) continue;
      active.delete(target);
      xhr.abort();
    }
  };
}
