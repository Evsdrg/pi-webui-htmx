// 代码高亮。按元素处理，不做全局扫描。
// 库本身很大，由调用方按需触发；本模块只提供挂载点。

const DONE = "data-highlighted";

export function mountHighlight(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>(`pre code:not([${DONE}])`);
  if (nodes.length === 0) return;
  void import("@/lib/hljs").then(({ highlightElement }) => {
    for (const el of nodes) {
      try {
        highlightElement(el);
        el.setAttribute(DONE, "1");
      } catch (err) {
        console.warn("[highlight]", err instanceof Error ? err.message : err);
      }
    }
  });
}
