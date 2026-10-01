// htmx 的 ESM 构建是 default 导出，不自动挂到 window。
// 我们在入口显式挂载，这里只为 window.htmx 补上类型声明。
import type htmx from "htmx.org";

declare global {
  interface Window {
    htmx: typeof htmx;
  }
}

export {};
