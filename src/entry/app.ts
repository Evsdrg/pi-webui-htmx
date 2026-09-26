// 入口：只放首屏必需的部分。重库通过动态 import 拆成惰性 chunk。
import htmx from "htmx.org";

// htmx 的 ESM 构建是 default 导出，不会自动挂到 window。
// 我们的模板与部分模块通过 window.htmx 访问它，这里显式挂载。
window.htmx = htmx;
import "@/styles/app.css";
import { mountHighlight } from "@/modules/highlight";
import { mountMath } from "@/modules/math";
import { mountMermaid } from "@/modules/mermaid";
import { mountAnsi } from "@/modules/ansi";
import { connectStream } from "@/modules/stream";
import { mountScroll } from "@/modules/scroll";
import { mountMarkdown } from "@/modules/markdown";

connectStream();
mountScroll();
mountMarkdown();

document.addEventListener("htmx:afterSwap", (event: Event) => {
  const target = event.target as HTMLElement | null;
  if (target) {
    mountMarkdown(target);
    mountHighlight(target);
    mountMath(target);
    mountMermaid(target);
    mountAnsi(target);
  }
});
