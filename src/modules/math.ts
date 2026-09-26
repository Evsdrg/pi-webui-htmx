// 仅检测到公式分隔符时加载 KaTeX；动态导入仍交给 Vite 解析。
export function mountMath(root: ParentNode = document): void {
  const nodes = Array.from(root.querySelectorAll<HTMLElement>('.markdown')).filter((node) => !node.dataset.mathRendered && /\$\$|\\\(|\\\[/.test(node.textContent ?? ''));
  if (!nodes.length) return;
  for (const node of nodes) node.dataset.mathRendered = 'pending';
  void Promise.all([import('katex/contrib/auto-render'), import('katex/dist/katex.min.css')]).then(([mod]) => {
    for (const node of nodes) {
      if (!node.isConnected) continue;
      mod.default(node, { delimiters: [{left:'$$',right:'$$',display:true},{left:'\\[',right:'\\]',display:true},{left:'\\(',right:'\\)',display:false}], throwOnError:false, trust:false, maxExpand:1000 });
      node.dataset.mathRendered = '1';
    }
  }).catch((error: unknown) => { for (const node of nodes) delete node.dataset.mathRendered; console.warn('公式渲染失败：', error); });
}
