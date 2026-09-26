// Markdown 渲染。marked + DOMPurify。
// DOMPurify 不是可选的：模型输出与文件内容都不可信，marked 不过滤 HTML。
//
// 这条模块被 entry 静态引用，但 marked/dompurify 通过动态 import 加载，
// Vite 会把它们拆成独立 chunk，首屏只含一个极小的加载器。

let pipeline: Promise<{
  parse(src: string): string;
  sanitize(html: string): string;
}> | null = null;

function load(): Promise<{ parse(src: string): string; sanitize(html: string): string }> {
  if (pipeline) return pipeline;
  pipeline = Promise.all([import("marked"), import("dompurify")]).then(([markedMod, purifyMod]) => {
    const DOMPurify = purifyMod.default;
    return {
      parse: (src: string) => markedMod.parse(src, { async: false, gfm: true, breaks: false }),
      sanitize: (html: string) => DOMPurify.sanitize(html, { USE_PROFILES: { html: true } }),
    };
  });
  return pipeline;
}

const RENDERED = "data-rendered";

/** 只渲染未处理过的节点；重复渲染会丢光标并造成闪烁。 */
export function mountMarkdown(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>(`.markdown:not([${RENDERED}])`);
  if (nodes.length === 0) return;
  void load()
    .then(({ parse, sanitize }) => {
      for (const el of nodes) {
        el.innerHTML = sanitize(parse(el.textContent ?? ""));
        el.setAttribute(RENDERED, "1");
      }
    })
    .catch((err: unknown) => {
      console.warn("[markdown]", err instanceof Error ? err.message : err);
    });
}
