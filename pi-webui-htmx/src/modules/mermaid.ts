// 图表只在出现 Mermaid 代码块时加载，使用严格模式和 SVG 净化。
let loading: Promise<typeof import('mermaid')> | null = null;
let serial = Promise.resolve();
export function mountMermaid(root: ParentNode = document): void {
  const nodes = Array.from(root.querySelectorAll<HTMLElement>('.mermaid:not([data-mermaid-rendered]),code.language-mermaid:not([data-mermaid-rendered])'));
  if (!nodes.length) return;
  for (const node of nodes) node.dataset.mermaidRendered = 'pending';
  loading ??= import('mermaid').catch((error: unknown) => { loading = null; throw error; });
  const task = loading;
  serial = serial.then(async () => {
    const [module, { default: purify }] = await Promise.all([task, import('dompurify')]);
    module.default.initialize({ startOnLoad:false, securityLevel:'strict', theme:document.documentElement.dataset.theme === 'dark' ? 'dark' : 'default', maxTextSize:50_000, flowchart:{htmlLabels:false} });
    for (const node of nodes) {
      if (!node.isConnected) continue;
      const source = node.textContent ?? '';
      try {
        if (source.length > 50_000) throw new Error('图表超出渲染上限');
        const { svg } = await module.default.render(`diagram-${crypto.randomUUID()}`, source);
        const target = node.matches('code') ? node.parentElement! : node;
        target.innerHTML = purify.sanitize(svg, { USE_PROFILES:{svg:true,svgFilters:true}, FORBID_TAGS:['foreignObject'] });
        target.className = 'mermaid'; target.dataset.mermaidRendered = '1';
      } catch (error) {
        node.dataset.mermaidRendered = 'failed';
        node.title = error instanceof Error ? `图表渲染失败：${error.message}` : '图表渲染失败';
      }
    }
  }).catch((error: unknown) => { for (const node of nodes) delete node.dataset.mermaidRendered; console.warn('图表库加载失败：', error); });
}
