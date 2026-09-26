import htmx from 'htmx.org';
import '@/styles/app.css';
import { mountScroll } from '@/modules/scroll';
import { mountLayout } from '@/modules/layout';
import { Workbench } from '@/modules/workbench';

window.htmx = htmx;
htmx.config.allowEval = false;
htmx.config.allowScriptTags = false;
htmx.config.historyCacheSize = 0;
const scroll = mountScroll();
const disposeLayout = mountLayout();
const app = new Workbench(scroll.bottom);
app.start();

async function render(root: ParentNode): Promise<void> {
  if (!root.querySelector('.markdown,.ansi')) return;
  const { mountMarkdown } = await import('@/modules/markdown');
  await mountMarkdown(root);
  if (root.querySelector('pre code')) (await import('@/modules/highlight')).mountHighlight(root);
  if (root.querySelector('.math, .math-inline, .math-display, .markdown')) (await import('@/modules/math')).mountMath(root);
  if (root.querySelector('.mermaid, .language-mermaid')) (await import('@/modules/mermaid')).mountMermaid(root);
  if (root.querySelector('.ansi')) (await import('@/modules/ansi')).mountAnsi(root);
}
document.addEventListener('htmx:afterSwap', (event) => {
  const root = (event as CustomEvent).detail?.target as HTMLElement | undefined;
  if (root) void render(root).catch((error: unknown) => console.warn('内容渲染失败：', error));
});
window.addEventListener('pagehide', () => { app.dispose(); scroll.dispose(); disposeLayout(); }, { once: true });
// BFCache 恢复时旧的连接与监听器已释放，重新装载以避免半可用页面。
window.addEventListener('pageshow', (event) => { if (event.persisted) location.reload(); });
