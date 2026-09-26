// 入口：只放首屏必需的部分。重库通过动态 import 拆成惰性 chunk。
import "htmx.org";
import "@/styles/app.css";
import { mountHighlight } from "@/modules/highlight";
import { mountMath } from "@/modules/math";
import { mountMermaid } from "@/modules/mermaid";
import { mountAnsi } from "@/modules/ansi";
import { connectStream } from "@/modules/stream";
import { mountMarkdown } from "@/modules/markdown";

connectStream();
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
