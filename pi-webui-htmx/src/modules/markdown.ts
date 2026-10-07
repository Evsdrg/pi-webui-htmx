// 模型输出是非可信文本，不能带入可触发桥命令的属性或控件。
type Engine = { parse(source: string): string; sanitize(html: string): string };
let pipeline: Promise<Engine> | null = null;
let engine: Engine | null = null;
async function load(): Promise<Engine> {
  if (!pipeline) pipeline = Promise.all([import('marked'), import('dompurify')]).then(([marked, purify]) => ({
    parse: (source: string) => marked.parse(source, { async: false, gfm: true }),
    sanitize: (html: string) => purify.default.sanitize(html, { USE_PROFILES: { html: true }, ALLOW_DATA_ATTR: false, FORBID_TAGS: ['form','input','button','textarea','select','style'], FORBID_ATTR: ['style','id','name'] }),
  })).catch((error: unknown) => { pipeline = null; throw error; });
  return pipeline;
}
export async function safeMarkdown(source: string): Promise<string> { const e = await load(); return e.sanitize(e.parse(source)); }

// warmMarkdown 预载解析引擎（marked + DOMPurify）。引擎就绪后 renderMarkdownSync
// 才能同步渲染；预热放在首帧之后，见 app.ts。
export async function warmMarkdown(): Promise<void> { engine = await load(); }

// renderMarkdownSync 用**已预载**的引擎同步渲染 root 内未处理的 .markdown 节点，
// 返回是否完成。它存在的理由：交换后的渲染若走 await（动态 import / 异步 mount），
// 原生 ESM 的 import 会跨任务边界，浏览器会把「原始 Markdown 文本」先绘制一帧、
// 再把「格式化结果」绘制一帧——切换会话时表现为正文先原始后格式化的一次可见跳变，
// 并把滚动位置一起顶动。预热后这里同步完成，全程不跨任务，只绘制一帧。
export function renderMarkdownSync(root: ParentNode = document): boolean {
  if (!engine) return false;
  const nodes = Array.from(root.querySelectorAll<HTMLElement>('.markdown:not([data-rendered])'));
  for (const node of nodes) {
    try {
      node.innerHTML = engine.sanitize(engine.parse(node.textContent ?? ''));
      for (const link of node.querySelectorAll('a')) link.rel = 'noopener noreferrer';
      node.dataset.rendered = '1';
    } catch { /* 单条渲染失败不影响其余，留待异步兜底重试 */ }
  }
  return true;
}

export async function mountMarkdown(root: ParentNode = document): Promise<void> {
  const nodes = Array.from(root.querySelectorAll<HTMLElement>('.markdown:not([data-rendered])'));
  if (!nodes.length) return;
  for (const node of nodes) node.dataset.rendered = 'pending';
  try {
    const e = await load();
    for (const node of nodes) {
      if (!node.isConnected) continue;
      node.innerHTML = e.sanitize(e.parse(node.textContent ?? ''));
      for (const link of node.querySelectorAll('a')) link.rel = 'noopener noreferrer';
      node.dataset.rendered = '1';
    }
  } catch (error) { for (const node of nodes) delete node.dataset.rendered; throw error; }
}
