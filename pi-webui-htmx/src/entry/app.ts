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

// 预热后的 markdown 模块引用：交换时优先走同步渲染（renderMarkdownSync），
// 避免原生动态 import 跨任务边界导致「原始文本先绘制一帧、格式化结果再绘制一帧」。
let markdownModule: typeof import('@/modules/markdown') | null = null;

async function render(root: HTMLElement): Promise<void> {
  // markdown 正文是内容高度的主要来源，且最常出现；引擎预热后同步渲染，
  // 让「原始文本 → 格式化」不产生可见跳变与随之的滚动顶动。
  if (markdownModule) {
    try { markdownModule.renderMarkdownSync(root); }
    catch (error) { console.warn('同步渲染 markdown 失败：', error); }
  }
  const run = async (selector: string, load: () => Promise<(root: ParentNode) => unknown>) => {
    if (!root.isConnected || !root.querySelector(selector)) return;
    const mount = await load();
    if (root.isConnected) await mount(root);
  };
  // 兜底：预热未就绪时仍用异步 mountMarkdown（找到已渲染的会跳过）。
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

// 首帧之后预热渲染模块。render() 每次交换都会按需 import 这些模块，而它们的重库
// （marked / DOMPurify、highlight.js、KaTeX）又是在模块内部再次按需 import；冷缓存
// 时每一步都是一次网络往返，内容因此分多段增高、滚动被反复顶动——切换会话时就是
// 可见的「来回闪」。prewarm 把 markdown 引擎与另两个模块载入内存，之后每次交换
// 都在同一帧内完成（markdown 更是同步渲染）。mermaid / ansi 依赖大且少见，保持按需。
function prewarm(): void {
  void (async () => {
    try {
      const m = await import('@/modules/markdown');
      await m.warmMarkdown();
      markdownModule = m;
    } catch (error) { console.warn('预热 markdown 失败：', error); }
    void import('@/modules/highlight');
    void import('@/modules/math');
  })();
}
if ('requestIdleCallback' in window) {
  (window as Window & { requestIdleCallback: (cb: () => void, opts?: { timeout: number }) => void })
    .requestIdleCallback(prewarm, { timeout: 3000 });
} else {
  setTimeout(prewarm, 800);
}
window.addEventListener('pagehide', () => { app.dispose(); scroll.dispose(); disposeLayout(); }, { once: true });
// BFCache 恢复时旧的连接与监听器已释放，重新装载以避免半可用页面。
window.addEventListener('pageshow', (event) => { if (event.persisted) location.reload(); });
