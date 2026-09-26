// 代码高亮。按元素处理，不做全局扫描。
// 库本身很大，由调用方按需触发；本模块只提供挂载点。

const DONE = "data-highlighted";

// 扩展名到 hljs 语言名的映射。不给提示时 hljs 按内容猜，
// 会把 Markdown 猜成 Python 之类，着色结果反而更差。
const LANGUAGES: Record<string, string> = {
  ts: "typescript", tsx: "typescript", mts: "typescript", cts: "typescript",
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  go: "go", rs: "rust", py: "python", rb: "ruby", java: "java", kt: "kotlin",
  c: "c", h: "c", cc: "cpp", cpp: "cpp", hpp: "cpp", cs: "csharp",
  css: "css", scss: "scss", less: "less", html: "xml", htm: "xml", xml: "xml",
  svg: "xml", vue: "xml", json: "json", jsonc: "json", yaml: "yaml", yml: "yaml",
  toml: "ini", ini: "ini", sh: "bash", bash: "bash", zsh: "bash",
  sql: "sql", md: "markdown", markdown: "markdown", diff: "diff", patch: "diff",
  dockerfile: "dockerfile", makefile: "makefile", lua: "lua", php: "php",
  swift: "swift", dart: "dart", ex: "elixir", erl: "erlang", hs: "haskell",
  pl: "perl", r: "r", scala: "scala", clj: "clojure", vim: "vim",
};

// languageFor 按文件名推断 hljs 语言名；未收录时返回空字符串，交给 hljs 自动判断。
export function languageFor(path: string): string {
  const base = (path.split("/").pop() ?? "").toLowerCase();
  if (base === "dockerfile" || base.startsWith("dockerfile.")) return "dockerfile";
  if (base === "makefile") return "makefile";
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return "";
  return LANGUAGES[base.slice(dot + 1)] ?? "";
}

// mountHighlight 高亮 root 内尚未处理的代码块。
// language 非空时作为语言提示写入 class，hljs 会优先采用而不是猜。
export function mountHighlight(root: ParentNode = document, language = ""): void {
  const nodes = root.querySelectorAll<HTMLElement>(`pre code:not([${DONE}]):not(.language-mermaid)`);
  if (nodes.length === 0) return;
  for (const el of nodes) {
    if (language && !el.className) { el.className = `language-${language}`; el.dataset.language = language; }
  }
  void import("@/lib/hljs").then(({ highlightElement }) => {
    for (const el of nodes) {
      if (!el.isConnected) continue;
      try {
        highlightElement(el);
        el.setAttribute(DONE, "1");
      } catch (err) {
        console.warn("[highlight]", err instanceof Error ? err.message : err);
      }
    }
  }).catch((err) => console.warn("代码高亮加载失败", err));
}
