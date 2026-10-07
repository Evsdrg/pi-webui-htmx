import { describe, expect, it } from 'vitest';
import { renderMarkdownSync, warmMarkdown } from '@/modules/markdown';

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
});
