// htmx 的全局类型。htmx.org 不自带 d.ts，这里只声明我们用到的部分。
declare global {
  interface Window {
    htmx: {
      /** 手动触发某个元素的 htmx 行为。 */
      trigger(element: Element | string, name: string, detail?: unknown): void;
      /** 手工处理新插入的 DOM。 */
      process(element: ParentNode): void;
      ajax(verb: string, path: string, options: { target?: string; swap?: string }): void;
      on(event: string, handler: (event: Event) => void): void;
      find(selector: string): Element | null;
      values(element: Element): Record<string, string>;
    };
  }
}
export {};
