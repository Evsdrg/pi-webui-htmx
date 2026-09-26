// 惰性内容：思考文本与工具结果图片。
//
// 历史页只渲染占位按钮，正文等点击才向桥取。这样每一页翻迁的带宽
// 只花在用户当时正在看的内容上——一条带 8 KB 思考 + 20 KB base64
// 图片的记录，不展开时为 0 字节。
//
// 用事件委托而不是给每个按钮绑监听：历史是整块替换的，
// 逐按钮绑定在每次翻页后都要重做一遍。

const el = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

function notice(message: string): void {
  document.body.dispatchEvent(new CustomEvent('pi-notify', { detail: { message, kind: 'warning' } }));
}

// wireLazy 在 shell 就绪后调用一次。
export function wireLazy(sessionId: () => string): void {
  document.addEventListener('click', (event) => {
    const button = (event.target as Element | null)?.closest<HTMLButtonElement>('.lazy-block');
    if (!button || button.dataset.loaded === '1') return;
    event.preventDefault();
    void load(button, sessionId());
  });
}

async function load(button: HTMLButtonElement, sessionId: string): Promise<void> {
  const kind = button.dataset.lazy ?? '';
  const entryId = button.dataset.entryId ?? '';
  const blockIndex = button.dataset.blockIndex ?? '';
  if (!sessionId || !entryId || blockIndex === '') { notice('占位符缺少必要参数'); return; }
  button.disabled = true;
  button.textContent = '加载中…';
  try {
    if (kind === 'thinking') {
      const response = await fetch(`/ui/sessions/${encodeURIComponent(sessionId)}/lazy?kind=thinking&entryId=${encodeURIComponent(entryId)}&blockIndex=${encodeURIComponent(blockIndex)}`, { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await errorText(response));
      const data = await response.json() as { thinking?: string };
      const box = document.createElement('div');
      box.className = 'lazy-thinking';
      // textContent 而不是 innerHTML：思考内容是模型输出，按不可信数据处理。
      box.textContent = data.thinking ?? '（空）';
      button.replaceWith(box);
      return;
    }
    if (kind === 'tool-image') {
      const response = await fetch(`/ui/sessions/${encodeURIComponent(sessionId)}/lazy?kind=tool-image&entryId=${encodeURIComponent(entryId)}&blockIndex=${encodeURIComponent(blockIndex)}`);
      if (!response.ok) throw new Error(await errorText(response));
      const blob = await response.blob();
      const image = document.createElement('img');
      image.className = 'lazy-image';
      image.alt = '工具结果图片';
      image.src = URL.createObjectURL(blob);
      button.replaceWith(image);
      return;
    }
    notice(`未知的惰性内容类型: ${kind}`);
  } catch (error) {
    button.disabled = false;
    button.textContent = '加载失败，点击重试';
    notice(error instanceof Error ? error.message : '加载失败');
  }
}

async function errorText(response: Response): Promise<string> {
  try {
    const data = await response.json() as { error?: { message?: string } };
    return data.error?.message ?? `请求失败（${response.status}）`;
  } catch {
    return `请求失败（${response.status}）`;
  }
}

export { el as lazyEl };
