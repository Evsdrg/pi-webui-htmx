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

async function render(root: HTMLElement): Promise<void> {
  const run = async (selector: string, load: () => Promise<(root: ParentNode) => unknown>) => {
    if (!root.isConnected || !root.querySelector(selector)) return;
    const mount = await load();
    if (root.isConnected) await mount(root);
  };
  await run('.markdown', async () => (await import('@/modules/markdown')).mountMarkdown);
  await run('pre code', async () => (await import('@/modules/highlight')).mountHighlight);
  await run('.math,.math-inline,.math-display,.markdown', async () => (await import('@/modules/math')).mountMath);
  await run('.mermaid,.language-mermaid', async () => (await import('@/modules/mermaid')).mountMermaid);
  await run('.ansi', async () => (await import('@/modules/ansi')).mountAnsi);
}
document.addEventListener('htmx:afterSwap', (event) => {
  const root = (event as CustomEvent).detail?.target as HTMLElement | undefined;
  if (root) void render(root).catch((error: unknown) => console.warn('内容渲染失败：', error));
});
window.addEventListener('pagehide', () => { app.dispose(); scroll.dispose(); disposeLayout(); }, { once: true });
// BFCache 恢复时旧的连接与监听器已释放，重新装载以避免半可用页面。
window.addEventListener('pageshow', (event) => { if (event.persisted) location.reload(); });
