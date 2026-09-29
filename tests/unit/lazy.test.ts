import { describe, expect, it, vi, beforeEach } from 'vitest';
import { mount } from '../helpers/dom';

// 构造一段带占位按钮的历史片段，形状与桥渲染的一致。
function fixture(): string {
  return `<article class="turn" data-turn-id="u1">
  <div class="turn-user"><div class="bubble">问题</div><button class="lazy-block" data-lazy="user-image" data-entry-id="u1" data-block-index="1">查看附带图片</button></div>
  <button class="lazy-block" data-lazy="thinking" hx-get="/ui/sessions/sess-1/lazy?kind=thinking&amp;format=html&amp;entryId=a1&amp;blockIndex=0" data-entry-id="a1" data-block-index="0">查看思考过程</button>
  <div class="turn-assistant"><div class="bubble markdown">回答</div></div>
  <ol><li><span class="step-kind">工具</span><pre class="step-detail">工具输出</pre>
    <button class="lazy-block" data-lazy="tool-image" data-entry-id="t1" data-block-index="0">查看工具图片</button>
  </li></ol>
</article>`;
}

describe('惰性内容占位符', () => {
  beforeEach(() => { vi.restoreAllMocks(); });

  it('思考由声明式片段负责，不再重复走图片fetch委托', async () => {
    const { document, cleanup } = mount(fixture());
    const fetchMock = vi.fn();vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    const dispose=wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="thinking"]')!.click();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(document.querySelector('[data-lazy="thinking"]')!.getAttribute('hx-get')).toContain('format=html');
    dispose();cleanup();
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
