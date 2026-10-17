// 复制链路的双格式与取数修正测试。
//
// 背景（2026-10-18，详见 pi-work/research-prompt/copy-format-20261018.md）：
// copyTurn 只写 text/plain，且纯文本取自渲染后 DOM 的 textContent——表格碎行、
// 随机空行、行尾硬换行双空格；贴进 Word 等富文本目标没有任何格式。
// 修复后要求：text/html（渲染后的语义结构）+ text/plain（innerText 形态、
// 去行尾硬换行空格）双格式；安全与非安全上下文都能带上 HTML。
//
// jsdom 没有 ClipboardItem/ClipboardEvent/DataTransfer（30.1.1 均为 undefined），
// 夹具里按真实规范的形态自行构造最小替身；这同时钉住了实现「按能力探测」的要求。
import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyRich, copyText } from '@/lib/clipboard';

/** FakeBlob / FakeDataTransfer / FakeClipboardItem：按浏览器真实行为的最小实现。 */
class FakeBlob {
  constructor(public parts: string[], public type: string) {}
  text(): Promise<string> { return Promise.resolve(this.parts.join('')); }
}
class FakeDataTransfer {
  private store = new Map<string, string>();
  setData(type: string, value: string): void { this.store.set(type, value); }
  getData(type: string): string { return this.store.get(type) ?? ''; }
}
class FakeClipboardItem {
  constructor(public entries: Record<string, FakeBlob>) {}
  get types(): string[] { return Object.keys(this.entries); }
  async getType(type: string): Promise<FakeBlob> {
    const blob = this.entries[type];
    if (!blob) throw new Error(`no ${type}`);
    return blob;
  }
}

type Written = Record<string, string>;

/** installSecureClipboard 替换全局为一套可观测的假剪贴板（安全上下文形态）。 */
function installSecureClipboard() {
  const written: Written[] = [];
  const fake = {
    Blob: FakeBlob,
    ClipboardItem: FakeClipboardItem,
    navigator: {
      clipboard: {
        write: async (items: FakeClipboardItem[]) => {
          for (const item of items) {
            const record: Written = {};
            for (const type of item.types) record[type] = await item.entries[type].text();
            written.push(record);
          }
        },
        writeText: async (value: string) => { written.push({ 'text/plain': value }); },
      },
    },
  };
  vi.stubGlobal('Blob', fake.Blob);
  vi.stubGlobal('ClipboardItem', fake.ClipboardItem);
  vi.stubGlobal('navigator', fake.navigator);
  return written;
}

/** installInsecure 模拟非安全上下文：无 clipboard API、无 ClipboardItem。 */
function installInsecure(execResult: boolean) {
  vi.stubGlobal('ClipboardItem', undefined);
  vi.stubGlobal('navigator', {} as Navigator);
  document.execCommand = vi.fn(() => execResult);
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.replaceChildren();
  delete (document as { execCommand?: unknown }).execCommand;
});

describe('copyRich 双格式写入（安全上下文）', () => {
  it('同时写入 text/plain 与 text/html 两种 flavor', async () => {
    const written = installSecureClipboard();
    const ok = await copyRich({ text: '标题\n正文', html: '<h1>标题</h1><p>正文</p>' });
    expect(ok).toBe(true);
    expect(written).toHaveLength(1);
    expect(written[0]['text/plain']).toBe('标题\n正文');
    expect(written[0]['text/html']).toBe('<h1>标题</h1><p>正文</p>');
  });

  it('ClipboardItem 没有时退回 copy 事件兜底且同样双格式', async () => {
    installSecureClipboard();
    vi.stubGlobal('ClipboardItem', undefined);
    vi.stubGlobal('navigator', {} as Navigator);
    // jsdom 没有 ClipboardEvent/DataTransfer：构造真实现形态的替身。
    // execCommand 不会真派发 copy 事件，所以直接手动构造并调用监听所走的流程——
    // 为了测到「监听里 setData」本身，这里用 addEventListener 真挂监听再手动派发。
    const transfers = new Map<string, string>();
    const dispatched: Array<Record<string, string>> = [];
    let listener: ((event: unknown) => void) | null = null;
    const originalAdd = document.addEventListener.bind(document);
    const addSpy = vi.spyOn(document, 'addEventListener').mockImplementation(((type: string, fn: (e: unknown) => void) => {
      if (type === 'copy') listener = fn;
      return originalAdd(type as never, fn as never);
    }) as typeof document.addEventListener);
    document.execCommand = vi.fn(() => {
      // 模拟浏览器：execCommand('copy') → 派发 copy 事件 → 监听往 clipboardData 写
      const record: Record<string, string> = { }
      Object.assign(record, {});
      const dt = {
        setData: (t: string, v: string) => { transfers.set(t, v); },
        getData: (t: string) => transfers.get(t) ?? '',
      };
      listener?.({ clipboardData: dt, preventDefault: () => {} });
      dispatched.push({ 'text/plain': dt.getData('text/plain'), 'text/html': dt.getData('text/html') });
      return true;
    });
    const ok = await copyRich({ text: '纯文本', html: '<p>富文本</p>' });
    expect(ok).toBe(true);
    expect(dispatched).toHaveLength(1);
    expect(dispatched[0]['text/plain']).toBe('纯文本');
    expect(dispatched[0]['text/html']).toBe('<p>富文本</p>');
    addSpy.mockRestore();
  });

  it('execCommand 报 false 时如实返回 false，不得抛异常', async () => {
    installInsecure(false);
    await expect(copyRich({ text: 'x', html: '<p>x</p>' })).resolves.toBe(false);
  });
});

describe('copyText 保持纯文本语义', () => {
  it('只写 text/plain，不写 text/html', async () => {
    const written = installSecureClipboard();
    await copyText('只有纯文本');
    expect(written).toHaveLength(1);
    expect(written[0]['text/html']).toBeUndefined();
    expect(written[0]['text/plain']).toBe('只有纯文本');
  });
});
