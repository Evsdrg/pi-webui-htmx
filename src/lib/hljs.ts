// highlight.js 的加载边界。单独一个文件，让 Vite 把它拆成独立 chunk，
// 且只有在真正出现代码块时才下载。
import hljs from "highlight.js/lib/core";
import plaintext from "highlight.js/lib/languages/plaintext";
import javascript from "highlight.js/lib/languages/javascript";
import typescript from "highlight.js/lib/languages/typescript";
import python from "highlight.js/lib/languages/python";
import go from "highlight.js/lib/languages/go";
import rust from "highlight.js/lib/languages/rust";
import json from "highlight.js/lib/languages/json";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import xml from "highlight.js/lib/languages/xml";
import markdown from "highlight.js/lib/languages/markdown";

for (const [name, def] of Object.entries({
  plaintext, javascript, typescript, python, go, rust, json, bash, css, xml, markdown,
})) {
  hljs.registerLanguage(name, def as never);
}

// 样式按需注入，避免首屏 CSS 带上整套主题。
const styleId = "hljs-theme";
if (!document.getElementById(styleId)) {
  void import("highlight.js/styles/github-dark.css?inline").then((mod) => {
    const style = document.createElement("style");
    style.id = styleId;
    style.textContent = mod.default as string;
    document.head.appendChild(style);
  });
}

export function highlightElement(el: HTMLElement): void {
  hljs.highlightElement(el as never);
}
