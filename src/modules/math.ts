// 公式渲染：KaTeX。
// KaTeX 约 90 KB，只在出现 .math 节点时才加载——本模块被 entry 静态引用，
// 但 katex 本身走动态 import，Vite 会拆成独立 chunk。
//
// 写法说明：用一个返回 Promise 的间接函数，避免打包器把依赖提升进首屏。

const DONE = "data-rendered";

type Katex = {
  renderToString(src: string, options: { displayMode: boolean; throwOnError: boolean; output: string }): string;
};

let loading: Promise<Katex> | null = null;

function load(): Promise<Katex> {
  loading ??= (async () => {
    const [katexMod] = await Promise.all([
      import(/* @vite-ignore */ "katex") as Promise<{ default: Katex }>,
      import(/* @vite-ignore */ "katex/dist/katex.min.css"),
    ]);
    return katexMod.default;
  })();
  return loading;
}

const DELIMITERS: Array<{ left: string; right: string; display: boolean }> = [
  { left: "$$", right: "$$", display: true },
  { left: "$", right: "$", display: false },
];

/** 只处理未渲染过的 .math 节点。 */
export function mountMath(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>(`.math:not([${DONE}])`);
  if (nodes.length === 0) return;
  void load()
    .then((katex) => {
      for (const el of nodes) {
        const src = el.textContent ?? "";
        const block = DELIMITERS.find((d) => src.startsWith(d.left) && src.endsWith(d.right));
        try {
          el.innerHTML = katex.renderToString(
            block ? src.slice(block.left.length, -block.right.length) : src,
            { displayMode: block?.display ?? false, throwOnError: false, output: "html" },
          );
        } catch (err) {
          console.warn("[math]", err instanceof Error ? err.message : err);
        } finally {
          el.setAttribute(DONE, "1");
        }
      }
    })
    .catch((err: unknown) => {
      console.warn("[math]", err instanceof Error ? err.message : err);
      for (const el of nodes) el.setAttribute(DONE, "1");
    });
}
