// 模型输出是非可信文本，不能带入可触发桥命令的属性或控件。
let pipeline: Promise<{ parse(source: string): string; sanitize(html: string): string }> | null = null;
async function load() {
  if (!pipeline) pipeline = Promise.all([import('marked'), import('dompurify')]).then(([marked, purify]) => ({
    parse: (source: string) => marked.parse(source, { async: false, gfm: true }),
    sanitize: (html: string) => purify.default.sanitize(html, { USE_PROFILES: { html: true }, ALLOW_DATA_ATTR: false, FORBID_TAGS: ['form','input','button','textarea','select','style'], FORBID_ATTR: ['style','id','name'] }),
  })).catch((error: unknown) => { pipeline = null; throw error; });
  return pipeline;
}
export async function safeMarkdown(source: string): Promise<string> { const engine = await load(); return engine.sanitize(engine.parse(source)); }
export async function mountMarkdown(root: ParentNode = document): Promise<void> {
  const nodes = Array.from(root.querySelectorAll<HTMLElement>('.markdown:not([data-rendered])'));
  if (!nodes.length) return;
  for (const node of nodes) node.dataset.rendered = 'pending';
  try {
    const engine = await load();
    for (const node of nodes) {
      if (!node.isConnected) continue;
      node.innerHTML = engine.sanitize(engine.parse(node.textContent ?? ''));
      for (const link of node.querySelectorAll('a')) link.rel = 'noopener noreferrer';
      node.dataset.rendered = '1';
    }
  } catch (error) { for (const node of nodes) delete node.dataset.rendered; throw error; }
}
