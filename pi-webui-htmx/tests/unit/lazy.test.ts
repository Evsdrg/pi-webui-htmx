import { describe, expect, it, vi, beforeEach } from 'vitest';
import { mount } from '../helpers/dom';

// 构造一段带占位的历史片段，形状与桥渲染的一致（2026-10-20 起
// 思考占位是 .thinking-body 上的 data 属性，没有按钮）。
function fixture(): string {
  return `<article class="turn" data-turn-id="u1">
  <div class="turn-user"><div class="bubble">问题</div><button class="lazy-block" data-lazy="user-image" data-entry-id="u1" data-block-index="1">查看附带图片</button></div>
  <details class="thinking-block"><summary><span class="thinking-preview">思考</span></summary><div class="thinking-body" data-lazy="thinking" data-entry-id="a1" data-block-index="0"></div></details>
  <div class="turn-assistant"><div class="bubble markdown">回答</div></div>
  <ol><li><span class="step-kind">工具</span><pre class="step-detail">工具输出</pre>
    <button class="lazy-block" data-lazy="tool-image" data-entry-id="t1" data-block-index="0">查看工具图片</button>
  </li></ol>
</article>`;
}

// 打开思考块。jsdom 会在 open 变化后**异步**派发 toggle（与浏览器一致），
// 这里只翻状态，断言一律用 waitFor 等事件落地。
function openThinking(document: Document): HTMLDetailsElement {
  const details = document.querySelector<HTMLDetailsElement>('details.thinking-block')!;
  details.open = true;
  return details;
}

function details_open_twice(document: Document): void {
  const details = document.querySelector<HTMLDetailsElement>('details.thinking-block')!;
  details.open = false;
  details.open = true;
}

describe('惰性内容占位符', () => {
  beforeEach(() => { vi.restoreAllMocks(); });

  it('展开思考块即自动载入，不再需要按钮', async () => {
    const { document, cleanup } = mount(fixture());
    // 模拟 htmx 真实行为：把服务端片段换入 target（成功判定看这个）。
    const ajax = vi.fn(async (_verb: string, _url: string, ctx: { target: HTMLElement }) => {
      ctx.target.innerHTML = '<div class="lazy-thinking markdown thinking-md">思考内容</div>';
    });
    (window as unknown as { htmx: { ajax: unknown } }).htmx = { ajax } as never;
    const fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    const dispose = wireLazy(() => 'sess-1');
    const body = document.querySelector<HTMLElement>('.thinking-body[data-lazy="thinking"]')!;
    openThinking(document);
    await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    const [verb, url, ctx] = ajax.mock.calls[0] as [string, string, { target: Element; swap: string }];
    expect(verb).toBe('get');
    expect(url).toContain('ui/sessions/sess-1/lazy?');
    expect(url).toContain('kind=thinking');
    expect(url).toContain('format=html');
    expect(url).toContain('entryId=a1');
    expect(url).toContain('blockIndex=0');
    expect(ctx.target).toBe(body);
    expect(ctx.swap).toBe('innerHTML');
    // 载入成功后标记完成：重复展开不再发请求。
    expect(fetchMock).not.toHaveBeenCalled();
    const details = document.querySelector<HTMLDetailsElement>('details.thinking-block')!;
    details.open = false;
    await new Promise((r) => setTimeout(r, 0));
    openThinking(document);
    await new Promise((r) => setTimeout(r, 0));
    expect(ajax).toHaveBeenCalledTimes(1);
    expect(body.dataset.loaded).toBe('1');
    dispose(); cleanup();
  });

  it('载入失败时提示可重试，再次展开会重新请求', async () => {
    const { document, cleanup } = mount(fixture());
    // htmx.ajax 失败但 resolve：正文没有换入任何元素即为失败（服务端
    // 成功时至少有 .lazy-thinking 或「（空）」占位）。
    const ajax = vi.fn(async () => {});
    (window as unknown as { htmx: { ajax: unknown } }).htmx = { ajax } as never;
    const { wireLazy } = await import('@/modules/lazy');
    const dispose = wireLazy(() => 'sess-1');
    const body = document.querySelector<HTMLElement>('.thinking-body[data-lazy="thinking"]')!;
    openThinking(document);
    await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(body.textContent).toContain('载入失败'));
    expect(body.dataset.loaded).toBeUndefined();
    // 再次展开：重新请求（重试路径）。
    details_open_twice(document);
    await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(2));
    dispose(); cleanup();
  });

  it('正在载入时重复展开不会叠加请求', async () => {
    const { document, cleanup } = mount(fixture());
    let release: (() => void) = () => {};
    const ajax = vi.fn(() => new Promise<void>((resolve) => { release = resolve; }));
    (window as unknown as { htmx: { ajax: unknown } }).htmx = { ajax } as never;
    const { wireLazy } = await import('@/modules/lazy');
    const dispose = wireLazy(() => 'sess-1');
    const details = document.querySelector<HTMLDetailsElement>('details.thinking-block')!;
    openThinking(document);
    await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(1));
    // 折叠再展开、重复派发：载入进行中一律不叠加。
    details.open = false;
    openThinking(document);
    openThinking(document);
    await new Promise((r) => setTimeout(r, 0));
    expect(ajax).toHaveBeenCalledTimes(1);
    release();
    dispose(); cleanup();
  });

  it('点工具图片占位符拉取二进制并换成 img', async () => {
    const { document, cleanup } = mount(fixture());
    // jsdom 不实现 createObjectURL，换成可预测的假 URL。
    URL.createObjectURL = vi.fn(() => 'blob:fake');
    const fetchMock = vi.fn(async () => new Response(new Uint8Array([1, 2, 3]), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="tool-image"]')!.click();
    await vi.waitFor(() => expect(document.querySelector('img.lazy-image')).not.toBeNull());
    expect(fetchMock.mock.calls[0][0]).toContain('kind=tool-image');
    expect(document.querySelector('img.lazy-image')!.getAttribute('src')).toMatch(/^blob:/);
    cleanup();
  });

  it('点用户附件图片占位符按用户条目取二进制', async () => {
    const { document, cleanup } = mount(fixture());
    URL.createObjectURL = vi.fn(() => 'blob:user-image');
    const fetchMock = vi.fn(async () => new Response(new Uint8Array([137, 80, 78, 71]), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="user-image"]')!.click();
    await vi.waitFor(() => expect(document.querySelector('img.lazy-image')).not.toBeNull());
    expect(fetchMock.mock.calls[0][0]).toContain('kind=user-image&entryId=u1&blockIndex=1');
    expect(document.querySelector('img.lazy-image')!.alt).toBe('用户附带图片');
    cleanup();
  });

  it('失败时按钮变为可重试，不抛异常', async () => {
    const { document, cleanup } = mount(fixture());
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { message: '条目不存在' } }), { status: 404, headers: { 'content-type': 'application/json' } })));
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    const button = document.querySelector<HTMLButtonElement>('[data-lazy="user-image"]')!;
    button.click();
    await vi.waitFor(() => expect(button.textContent).toBe('加载失败，点击重试'));
    expect(button.disabled).toBe(false);
    cleanup();
  });

  it('重复点击已加载的占位符不再发请求', async () => {
    const { document, cleanup } = mount(fixture());
    URL.createObjectURL = vi.fn(() => 'blob:once');
    const fetchMock = vi.fn(async () => new Response('image'));
    vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="user-image"]')!.click();
    await vi.waitFor(() => expect(document.querySelector('.lazy-image')).not.toBeNull());
    document.querySelector('.lazy-image')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    cleanup();
  });
});

describe('文件查看器按类型分流', () => {
  it('含 ANSI 转义时挂 .ansi 且跳过语法高亮', async () => {
    const { document, cleanup } = mount(`<div id=file-preview hidden><pre><code id=file-content></code></pre></div>`);
    // 直接验证 ansi 模块的行为：转义序列必须变成带色 span。
    const { mountAnsi } = await import('@/modules/ansi');
    const code = document.getElementById('file-content')!;
    code.textContent = '\u001b[31mRED\u001b[0m plain';
    code.classList.add('ansi');
    mountAnsi(document.getElementById('file-preview')!);
    expect(code.querySelectorAll('span[style]').length).toBeGreaterThan(0);
    expect(code.textContent).toContain('RED');
    // 已渲染过的节点不重复处理。
    const before = code.innerHTML;
    mountAnsi(document.getElementById('file-preview')!);
    expect(code.innerHTML).toBe(before);
    cleanup();
  });
});
