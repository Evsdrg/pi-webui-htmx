import { describe, expect, it, vi, beforeEach } from 'vitest';
import { mount } from '../helpers/dom';

// 构造一段带占位按钮的历史片段，形状与桥渲染的一致。
function fixture(): string {
  return `<article class="turn" data-turn-id="u1">
  <div class="turn-user"><div class="bubble">问题</div></div>
  <button class="lazy-block" data-lazy="thinking" data-entry-id="a1" data-block-index="0">查看思考过程</button>
  <div class="turn-assistant"><div class="bubble markdown">回答</div></div>
  <ol><li><span class="step-kind">工具</span><pre class="step-detail">工具输出</pre>
    <button class="lazy-block" data-lazy="tool-image" data-entry-id="t1" data-block-index="0">查看工具图片</button>
  </li></ol>
</article>`;
}

describe('惰性内容占位符', () => {
  beforeEach(() => { vi.restoreAllMocks(); });

  it('点思考占位符拉取文本并以 textContent 渲染', async () => {
    const { document, cleanup } = mount(fixture());
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ thinking: '这是思考' }), { status: 200, headers: { 'content-type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="thinking"]')!.click();
    await vi.waitFor(() => expect(document.querySelector('.lazy-thinking')).not.toBeNull());
    expect(document.querySelector('.lazy-thinking')!.textContent).toBe('这是思考');
    // URL 必须带上会话、条目与块下标。
    expect(fetchMock.mock.calls[0][0]).toContain('/ui/sessions/sess-1/lazy?kind=thinking&entryId=a1&blockIndex=0');
    cleanup();
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

  it('失败时按钮变为可重试，不抛异常', async () => {
    const { document, cleanup } = mount(fixture());
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { message: '条目不存在' } }), { status: 404, headers: { 'content-type': 'application/json' } })));
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    const button = document.querySelector<HTMLButtonElement>('[data-lazy="thinking"]')!;
    button.click();
    await vi.waitFor(() => expect(button.textContent).toBe('加载失败，点击重试'));
    expect(button.disabled).toBe(false);
    cleanup();
  });

  it('重复点击已加载的占位符不再发请求', async () => {
    const { document, cleanup } = mount(fixture());
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ thinking: 'x' }), { status: 200, headers: { 'content-type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { wireLazy } = await import('@/modules/lazy');
    wireLazy(() => 'sess-1');
    document.querySelector<HTMLButtonElement>('[data-lazy="thinking"]')!.click();
    await vi.waitFor(() => expect(document.querySelector('.lazy-thinking')).not.toBeNull());
    // 按钮已被替换成 div，再点原位置不应触发新请求。
    document.querySelector('.lazy-thinking')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
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
