// 图表：mermaid 约 2.5 MB，必须动态 import 且只在出现节点时加载。
// 首屏绝不包含它——Vite 会把它拆成独立 chunk。

const DONE = "data-rendered";

// mermaid 的类型与运行时都通过运行时 import 取得。
// 写成 /* @vite-ignore */ 之外的方式会让打包器把它并入首屏，
// 这里用一个间接的动态 import，确保它成为独立 chunk。
type Mermaid = {
  initialize(config: Record<string, unknown>): void;
  render(id: string, src: string): Promise<{ svg: string }>;
};

let loading: Promise<Mermaid> | null = null;

function load(): Promise<Mermaid> {
  loading ??= (async () => {
    const mod = (await import(/* @vite-ignore */ "mermaid")) as unknown as { default: Mermaid };
    mod.default.initialize({ startOnLoad: false, theme: "dark", securityLevel: "strict" });
    return mod.default;
  })();
  return loading;
}

export function mountMermaid(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>(`.mermaid:not([${DONE}])`);
  if (nodes.length === 0) return;
  void load()
    .then(async (mermaid) => {
      let i = 0;
      for (const el of nodes) {
        const id = `mmd-${Date.now()}-${i++}`;
        try {
          const { svg } = await mermaid.render(id, el.textContent ?? "");
          el.innerHTML = svg;
        } catch (err) {
          console.warn("[mermaid]", err instanceof Error ? err.message : err);
        } finally {
          el.setAttribute(DONE, "1");
        }
      }
    })
    .catch((err: unknown) => {
      console.warn("[mermaid]", err instanceof Error ? err.message : err);
      for (const el of nodes) el.setAttribute(DONE, "1");
    });
}
