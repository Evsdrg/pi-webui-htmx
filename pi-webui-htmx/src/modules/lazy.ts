// 思考正文由 htmx 加载服务端 HTML；图片 Blob 与挂载作用域一起释放。
function notice(message: string): void {
  document.body.dispatchEvent(new CustomEvent('pi-notify', { detail: { message, kind: 'warning' } }));
}

export function wireLazy(sessionId: () => string, parent?: AbortSignal): () => void {
  const scope = new AbortController();
  const pending = new Map<HTMLElement, AbortController>();
  const images = new Map<HTMLElement, string>();
  const release = (image: HTMLElement) => {
    const url = images.get(image);
    if (url) URL.revokeObjectURL(url);
    images.delete(image);
  };
  const dispose = () => {
    scope.abort();
    for (const controller of pending.values()) controller.abort();
    pending.clear();
    for (const image of images.keys()) release(image);
    parent?.removeEventListener('abort', dispose);
  };
  if (parent?.aborted) { dispose(); return dispose; }
  parent?.addEventListener('abort', dispose, { once: true });

  document.addEventListener('htmx:beforeCleanupElement', (event) => {
    const root = (event as CustomEvent).detail?.elt as HTMLElement | undefined;
    if (!root) return;
    for (const [button, controller] of pending) {
      if (root === button || root.contains(button)) controller.abort();
    }
    for (const image of images.keys()) {
      if (root === image || root.contains(image)) release(image);
    }
  }, { signal: scope.signal });

  document.addEventListener('click', (event) => {
    const button = (event.target as Element | null)?.closest<HTMLButtonElement>('.lazy-block');
    if (!button || button.disabled || pending.has(button)) return;
    event.preventDefault();
    void loadImage(button);
  }, { signal: scope.signal });

  // 思考块：展开即载入（2026-10-20 重设计）。旧设计在正文里放一个
  // 「载入思考内容」按钮，用户要多点一下才能看到内容——多此一举。
  // 但「按需拉取」本身必须保留：单块思考最大可到数十万字符
  // （本机会话实测 50 万），预载进历史片段会把片段撑爆。
  // 现在把占位挪到 .thinking-body 上，details 展开时自动经 htmx.ajax
  // 拉取：htmx 负责交换与事件，markdown 渲染管线（afterSwap 钩子）照常生效。
  //
  // toggle 事件不冒泡，只能在捕获阶段接到。
  const thinkingPending = new WeakSet<HTMLElement>();
  document.addEventListener('toggle', (event) => {
    const details = event.target as HTMLDetailsElement;
    if (!details.matches?.('details.thinking-block') || !details.open) return;
    const body = details.querySelector<HTMLElement>('.thinking-body[data-lazy="thinking"]');
    if (!body || body.dataset.loaded === '1' || thinkingPending.has(body)) return;
    void loadThinking(body);
  }, { capture: true, signal: scope.signal });

  async function loadThinking(body: HTMLElement): Promise<void> {
    const session = sessionId();
    const { entryId = '', blockIndex = '' } = body.dataset;
    if (!session || !entryId || blockIndex === '') {
      body.textContent = '占位符缺少必要参数';
      return;
    }
    thinkingPending.add(body);
    try {
      const query = new URLSearchParams({ kind: 'thinking', format: 'html', entryId, blockIndex });
      // 相对路径：云端外壳注入 <base href="/d/{id}/">，根绝对路径会绕过设备前缀（B54）。
      await window.htmx.ajax('get', `ui/sessions/${encodeURIComponent(session)}/lazy?${query}`, { target: body, swap: 'innerHTML' });
      // htmx.ajax 在 HTTP 错误时也可能正常 resolve：服务端成功时一定换入了
      // .lazy-thinking（空思考也有「（空）」占位），以此为准判断成败。
      if (!body.firstElementChild) body.textContent = '载入失败，重新展开可重试';
      else body.dataset.loaded = '1';
    } catch {
      if (!scope.signal.aborted && body.isConnected) body.textContent = '载入失败，重新展开可重试';
    } finally {
      thinkingPending.delete(body);
    }
  }

  async function loadImage(button: HTMLButtonElement): Promise<void> {
    const session = sessionId();
    const { lazy: kind = '', entryId = '', blockIndex = '' } = button.dataset;
    if (!session || !entryId || blockIndex === '') {
      notice('占位符缺少必要参数');
      return;
    }
    const controller = new AbortController();
    pending.set(button, controller);
    button.disabled = true;
    button.textContent = '加载中…';
    const alive = () => !scope.signal.aborted && !controller.signal.aborted && button.isConnected && session === sessionId();
    try {
      if (kind !== 'tool-image' && kind !== 'user-image') throw new Error(`未知的惰性内容类型: ${kind}`);
      const query = new URLSearchParams({ kind, entryId, blockIndex });
      // 相对路径：云端外壳注入 <base href="/d/{id}/">，根绝对路径会绕过设备前缀。
      const response = await fetch(`ui/sessions/${encodeURIComponent(session)}/lazy?${query}`, { signal: controller.signal });
      if (!response.ok) throw new Error(await errorText(response));
      const blob = await response.blob();
      if (!alive()) return;
      const image = document.createElement('img');
      image.className = 'lazy-image';
      image.alt = kind === 'user-image' ? '用户附带图片' : '工具结果图片';
      const url = URL.createObjectURL(blob);
      images.set(image, url);
      image.addEventListener('load', () => release(image), { once: true });
      image.addEventListener('error', () => release(image), { once: true });
      image.src = url;
      button.replaceWith(image);
    } catch (error) {
      if (!alive()) return;
      button.disabled = false;
      button.textContent = '加载失败，点击重试';
      notice(error instanceof Error ? error.message : '加载失败');
    } finally {
      pending.delete(button);
    }
  }
  return dispose;
}

async function errorText(response: Response): Promise<string> {
  try {
    const data = await response.json() as { error?: { message?: string } };
    return data.error?.message ?? `请求失败（${response.status}）`;
  } catch {
    return `请求失败（${response.status}）`;
  }
}
