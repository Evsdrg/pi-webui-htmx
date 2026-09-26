// ANSI 文本转 HTML。bash 输出与会话日志用。
import { AnsiUp } from "ansi_up";

const converter = new AnsiUp();
const DONE = "data-rendered";

/** 把 .ansi 节点的文本转成着色的 HTML。输入不可信，仅使用 ansi_up 的转义输出。 */
export function mountAnsi(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>(`.ansi:not([${DONE}])`);
  for (const el of nodes) {
    el.innerHTML = converter.ansi_to_html(el.textContent ?? "");
    el.setAttribute(DONE, "1");
  }
}
