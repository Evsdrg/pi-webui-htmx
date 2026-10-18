import { describe, expect, it } from 'vitest';
import { mountMarkdown, renderMarkdownSync, warmMarkdown } from '@/modules/markdown';

// 切换会话时正文曾分两帧绘制（先原始 Markdown、再格式化），把滚动位置顶得来回跳。
// renderMarkdownSync 用预热好的引擎同步渲染，全程不跨任务边界，只绘制一帧。
describe('markdown 同步渲染（切会话不再先原始后格式化）', () => {
  it('预热前不可用，预热后同步渲染并标记 data-rendered', async () => {
    const box = document.createElement('div');
    box.innerHTML = '<div class="markdown">**粗体** 与 `代码`</div>';
    // 引擎未预热时不能同步渲染，调用方应退回异步路径。
    expect(renderMarkdownSync(box)).toBe(false);
    await warmMarkdown();
    expect(renderMarkdownSync(box)).toBe(true);
    const node = box.querySelector<HTMLElement>('.markdown')!;
    expect(node.dataset.rendered).toBe('1');
    expect(node.querySelector('strong')?.textContent).toBe('粗体');
    expect(node.querySelector('code')?.textContent).toBe('代码');
  });

  it('已渲染的节点不重复处理', async () => {
    await warmMarkdown();
    const box = document.createElement('div');
    box.innerHTML = '<div class="markdown" data-rendered="1">**不应改动**</div>';
    expect(renderMarkdownSync(box)).toBe(true);
    // 带 data-rendered 的节点被选择器排除，内容保持原样。
    expect(box.querySelector<HTMLElement>('.markdown')!.textContent).toBe('**不应改动**');
  });

  // hx-swap="outerHTML" 换入的节点**本身**可能就是 .markdown 容器
  // （思考正文就是这样：按钮被 <div class="lazy-thinking markdown"> 替换）。
  // 旧实现只在 root 的后代里找，换入的根节点自己永远不会被渲染。
  it('root 自身就是 markdown 容器时也要渲染', async () => {
    await warmMarkdown();
    const node = document.createElement('div');
    node.className = 'markdown';
    node.textContent = '**重点** 与 `代码`';
    expect(renderMarkdownSync(node)).toBe(true);
    expect(node.dataset.rendered).toBe('1');
    expect(node.querySelector('strong')?.textContent).toBe('重点');
  });

  it('mountMarkdown 对 root 自身同样生效', async () => {
    await warmMarkdown();
    // 真实场景来自 htmx 换入：节点已在文档里。未挂载的节点按设计跳过。
    const host = document.createElement('div');
    document.body.append(host);
    const node = document.createElement('div');
    node.className = 'markdown';
    node.textContent = '**重点**';
    host.append(node);
    await mountMarkdown(node);
    expect(node.dataset.rendered).toBe('1');
    expect(node.querySelector('strong')?.textContent).toBe('重点');
    host.remove();
  });
});
