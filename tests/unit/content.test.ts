import { afterEach, expect, it, vi } from 'vitest';
import { safeMarkdown, mountMarkdown } from '../../src/modules/markdown';
import { showToast, clearToasts } from '../../src/modules/toast';
afterEach(() => { clearToasts(); vi.useRealTimers(); document.body.replaceChildren(); });
it('保留 Markdown 排版，移除脚本和桥操作属性', async () => {
  const html = await safeMarkdown('**正文** <img src=x onerror=alert(1)><button data-action=delete>删除</button><a href="javascript:alert(1)" data-session=secret hx-get="/ui/files">链接</a><script>alert(1)</script>');
  const node = document.createElement('div'); node.innerHTML = html;
  expect(node.querySelector('strong')?.textContent).toBe('正文');
  expect(node.querySelector('script,button,[onerror],[hx-get],[data-session]')).toBeNull();
  expect(node.querySelector('a')?.getAttribute('href')).toBeNull();
});
it('重复挂载不重新解析已有 Markdown', async () => {
  document.body.innerHTML = '<div class=markdown>**hello**</div>';
  await mountMarkdown(); const strong = document.querySelector('strong');
  await mountMarkdown(); expect(document.querySelector('strong')).toBe(strong);
});
it('通知刷屏时节点和计时器均保持四个', () => {
  vi.useFakeTimers(); for (let i=0;i<200;i++) showToast('<script>正文</script>');
  expect(document.querySelectorAll('.toast')).toHaveLength(4); expect(document.querySelector('script')).toBeNull(); expect(vi.getTimerCount()).toBe(4);
  vi.advanceTimersByTime(6001); expect(document.querySelectorAll('.toast')).toHaveLength(0); expect(vi.getTimerCount()).toBe(0);
});
