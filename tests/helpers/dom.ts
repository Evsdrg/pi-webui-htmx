import { JSDOM } from 'jsdom';

// mount 在隔离的 jsdom 里装一段 HTML。
// vitest 的 environment 已是 jsdom，这里只为每个用例换一份独立 document：
// 某些全局（document/window）只有 getter，必须用 defineProperty 覆盖。
export function mount(html: string): { window: Window & typeof globalThis; document: Document; cleanup(): void } {
  const dom = new JSDOM(`<!doctype html><html><body>${html}</body></html>`, { url: 'http://127.0.0.1:39142/' });
  const { window } = dom;
  const globals = globalThis as unknown as Record<string, unknown>;
  const saved: [string, PropertyDescriptor | undefined][] = [];
  for (const key of ['document', 'HTMLElement', 'Element', 'Node', 'DragEvent', 'DataTransfer', 'ClipboardEvent', 'FileReader', 'File']) {
    saved.push([key, Object.getOwnPropertyDescriptor(globals, key)]);
    Object.defineProperty(globals, key, { value: (window as unknown as Record<string, unknown>)[key], configurable: true, writable: true });
  }
  return {
    window: window as never,
    document: window.document,
    cleanup() {
      for (const [key, descriptor] of saved) {
        if (descriptor) Object.defineProperty(globals, key, descriptor);
        else delete globals[key];
      }
      window.close();
    },
  };
}
