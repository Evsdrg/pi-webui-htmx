// htmx 交换结果的根节点选择（纯函数，无依赖，可进首屏包）。
//
// hx-swap 的类型决定 detail 里哪个节点是「换入的内容」：
//   - outerHTML：新节点在 detail.elt，detail.target 是已被替换掉的旧节点，
//     它通常已脱离文档（思考正文就是这样：按钮被
//     <div class="lazy-thinking markdown"> 整个替换）。
//   - innerHTML 等：新内容是 detail.target 的后代。
// 两个都返回，由调用方去重；渲染管线会跳过已脱离文档的节点。
export function swapRoots(detail: unknown): HTMLElement[] {
  const d = detail as { target?: unknown; elt?: unknown } | undefined;
  const out: HTMLElement[] = [];
  for (const node of [d?.elt, d?.target]) {
    if (node instanceof HTMLElement && !out.includes(node)) out.push(node);
  }
  return out;
}
