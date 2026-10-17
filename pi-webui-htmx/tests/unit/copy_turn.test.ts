// copyTurn 的取数修正测试。
//
// 旧实现取 .bubble 的 textContent 拼接：表格碎成每格一行、段间空行数随机、
// markdown 硬换行双空格残留。新实现：
//   text/html —— 已渲染（data-rendered）的气泡直接取 innerHTML（渲染时已消毒）；
//   text/plain —— innerText 形态 + 去行尾硬换行空格。
// 未渲染的气泡（流式中）html 侧回退为转义文本，plain 侧用 textContent 原样。
import { afterEach, describe, expect, it } from 'vitest';
import { collectTurnCopy } from '@/modules/commands';

afterEach(() => { document.body.replaceChildren(); });

/** mountTurn 拼一个「已渲染」的助手气泡，结构对照隔离实例的真实 DOM。 */
function mountTurn(inner: string, rendered = true): HTMLElement {
  document.body.innerHTML = `<article class="turn"><div class="turn-assistant"><span class="role">π</span><div class="bubble markdown"${rendered ? ' data-rendered="1"' : ''}>${inner}</div></div><div class="turn-actions"><button data-action="copy-turn">复制</button></div></article>`;
  return document.querySelector<HTMLElement>('[data-action="copy-turn"]')!;
}

describe('collectTurnCopy 取数', () => {
  it('已渲染气泡：html 为语义结构，plain 走文本序列化并去硬换行空格', () => {
    const button = mountTurn('<h2>测试标题</h2><p>这是<strong>粗体</strong>段落。  </p><ul><li>第一行</li><li>第二行</li></ul>');
    const result = collectTurnCopy(button);
    // html 侧保留 innerHTML 原样（含行尾空格——HTML 语义里它们无害，Word 会折叠）；
    // 注意断言的是结构标签，而不是逐字节一致。
    expect(result.html).toContain('<h2>测试标题</h2>');
    expect(result.html).toContain('<strong>粗体</strong>');
    expect(result.html).toContain('<ul><li>第一行</li><li>第二行</li></ul>');
    // jsdom 没有 innerText，退回 textContent；清理仍要去掉行尾双空格。
    // （innerText 形态——表格行 tab、块间空行——由浏览器实测覆盖，见研究报告。）
    expect(result.text).toBe('测试标题这是粗体段落。\n第一行第二行');
    expect(result.text).toContain('粗体');
    expect(result.text).toContain('第一行');
  });

  it('未渲染气泡（流式中）：html 回退转义文本，plain 原样', () => {
    const button = mountTurn('## 原始 markdown &lt;tag&gt;', false);
    const result = collectTurnCopy(button);
    expect(result.html).toBe('<p>## 原始 markdown &lt;tag&gt;</p>');
    expect(result.text).toBe('## 原始 markdown <tag>');
  });

  it('多个气泡间以空行分隔，工具说明段也一并进入', () => {
    document.body.innerHTML = `<article class="turn">
      <div class="turn-assistant"><div class="bubble markdown" data-rendered="1"><p>先说明一下。</p></div></div>
      <div class="turn-assistant"><div class="bubble markdown process-text" data-rendered="1"><p>（工具期间的话）</p></div></div>
      <div class="turn-assistant"><div class="bubble markdown" data-rendered="1"><p>结论在此。</p></div></div>
      <div class="turn-actions"><button data-action="copy-turn">复制</button></div>
    </article>`;
    const button = document.querySelector<HTMLElement>('[data-action="copy-turn"]')!;
    const result = collectTurnCopy(button);
    expect(result.text).toBe('先说明一下。\n\n（工具期间的话）\n\n结论在此。');
    expect(result.html).toContain('<p>先说明一下。</p>');
    expect(result.html).toContain('<p>结论在此。</p>');
  });

  it('气泡为空时不产生空段', () => {
    const button = mountTurn('');
    const result = collectTurnCopy(button);
    expect(result.text).toBe('');
    expect(result.html).toBe('');
  });
});
