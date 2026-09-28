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

// 配色跟随主题：用我们自己的样式表（src/styles/code.css，由本模块引入，
// 因此与高亮代码一起按需下载）。以前这里注入 highlight.js 的 github-dark
// 主题，它在四个浅色主题下会留下一块深色代码背景 + 浅灰字（正文对比度
// 只有 1.5:1），且不受主题切换影响。
import "@/styles/code.css";

export function highlightElement(el: HTMLElement): void {
  hljs.highlightElement(el as never);
}
