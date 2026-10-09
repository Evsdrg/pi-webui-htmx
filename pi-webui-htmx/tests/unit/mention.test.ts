import { describe, expect, it, vi } from 'vitest';
import { detect, apply, FileCompleter } from '@/modules/mention';

describe('@ 触发识别', () => {
  it('行首 @ 后跟查询词应触发', () => {
    expect(detect('@rea', 4)).toEqual({ start: 0, query: 'rea' });
  });
  it('空白后的 @ 应触发', () => {
    expect(detect('请看 @src/ma', 11)).toEqual({ start: 3, query: 'src/ma' });
  });
  it('夹在单词中间的 @ 不触发', () => {
    // "user@host" 这种邮箱写法：@ 前不是空白。
    expect(detect('write to user@host now', 15)).toBeNull();
  });
  it('@ 后有空格不触发', () => {
    expect(detect('@rea done', 8)).toBeNull();
  });
  it('没有 @ 不触发', () => {
    expect(detect('普通句子', 4)).toBeNull();
  });
  it('光标在 @ 之前不触发', () => {
    expect(detect('@rea', 0)).toBeNull();
  });
  it('取光标前最近的一个 @', () => {
    expect(detect('@first @sec', 12)).toEqual({ start: 7, query: 'sec' });
  });
  it('at 指错位置时往前找 @，不产出坏文本', () => {
    // 传了 query 中间的下标：应校正到 @ 处，而不是拼出 "@a@b"。
    const out = apply('@abcd', 2, 'abc.txt');
    expect(out.value).toBe('@abc.txt ');
    expect(out.value).not.toContain('@a@');
  });
});

describe('插入选中路径', () => {
  it('替换 @query 段并留一个空格', () => {
    const out = apply('请看 @rea 末尾', 3, 'README.md');
    expect(out.value).toBe('请看 @README.md 末尾');
    expect(out.caret).toBe('请看 @README.md '.length);
  });
  it('光标后的内容不被吃掉', () => {
    // at 指向 @；@query 后面的内容必须原样保留。
    const out = apply('@abcd', 0, 'abc.txt');
    expect(out.value).toBe('@abc.txt ');
  });
  it('行首插入', () => {
    const out = apply('@', 0, 'src/main.go');
    expect(out.value).toBe('@src/main.go ');
  });
});

describe('Enter 不得把半个查询提交出去', () => {
  it('菜单开着但无候选时 choose 返回 false，调用方仍须消费按键', async () => {
    const { mount } = await import('../helpers/dom');
    const { document, cleanup } = mount(`<textarea id=prompt></textarea><div id=mention-menu hidden></div>`);
    const { FileCompleter } = await import('@/modules/mention');
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    const menu = document.getElementById('mention-menu')!;
    // 搜索永远返回空：模拟项目里没有匹配文件。
    const completer = new FileCompleter(input, menu, () => '/w', async () => []);
    input.value = '@zzz';
    input.setSelectionRange(4, 4);
    completer.refresh();
    await new Promise((r) => setTimeout(r, 250));
    expect(completer.active).toBe(true);
    expect(completer.choose()).toBe(false);
    // 输入框内容不被改动——没有被插入，也没有被清空。
    expect(input.value).toBe('@zzz');
    cleanup();
  });

  it('菜单元素缺失时自我禁用，不抛异常', async () => {
    const { mount } = await import('../helpers/dom');
    const { document, cleanup } = mount(`<textarea id=prompt></textarea>`);
    const { FileCompleter } = await import('@/modules/mention');
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    const completer = new FileCompleter(input, null, () => '/w', async () => []);
    expect(() => completer.refresh()).not.toThrow();
    expect(completer.active).toBe(false);
    expect(completer.choose()).toBe(false);
    expect(completer.move(1)).toBe(false);
    cleanup();
  });
});

describe('补全请求的归属', () => {
  it('已有候选时连续输入保留菜单容器，避免每次结果返回重播入场', async () => {
    vi.useFakeTimers();
    const input = document.createElement('textarea');
    const menu = document.createElement('div');
    menu.hidden = true;
    const search = vi.fn().mockResolvedValue([{ path: 'README.md' }]);
    const completer = new FileCompleter(input, menu, () => '/w', search);
    try {
      input.value = '@r'; input.setSelectionRange(2, 2);
      completer.refresh();
      expect(menu.hidden).toBe(true);
      await vi.advanceTimersByTimeAsync(120);
      expect(menu.hidden).toBe(false);
      input.value = '@re'; input.setSelectionRange(3, 3);
      completer.refresh();
      expect(menu.hidden).toBe(false);
      expect(menu.textContent).toContain('正在查找');
      expect(completer.choose()).toBe(false);
      expect(menu.querySelector('button')).toBeNull();
      await vi.advanceTimersByTimeAsync(120);
      expect(menu.hidden).toBe(false);
      expect(menu.textContent).toBe('README.md');
      search.mockResolvedValue([]);
      input.value = '@zzz'; input.setSelectionRange(4, 4);
      completer.refresh();
      await vi.advanceTimersByTimeAsync(120);
      expect(menu.hidden).toBe(true);
    } finally {
      completer.hide();
      vi.useRealTimers();
    }
  });

  // U09：refresh 只在新 load 开始时递增 seq，debounce 窗口内
  // 回来的旧候选仍会写进新菜单。
  it('防抖窗口内的旧结果被丢弃', async () => {
    vi.useFakeTimers();
    try {
      const input = document.createElement('textarea');
      const menu = document.createElement('div');
      document.body.append(input, menu);
      let resolveFirst!: (v: { path: string }[]) => void;
      const search = vi.fn()
        .mockImplementationOnce(() => new Promise<{ path: string }[]>((r) => { resolveFirst = r; }))
        .mockImplementation(() => Promise.resolve([{ path: 'new.ts' }]));
      const completer = new FileCompleter(input, menu, () => '/w', search as never);

      // 第一次请求：进入防抖并真正发出。
      input.value = '@a';
      input.setSelectionRange(2, 2);
      completer.refresh();
      await vi.advanceTimersByTimeAsync(300);
      expect(search).toHaveBeenCalledTimes(1);

      // 防抖窗口内用户又输入：query 已变，但第二次 load 还没被触发。
      input.value = '@ab';
      input.setSelectionRange(3, 3);
      completer.refresh();

      // 旧请求此刻才回来——必须被丢弃，不能写进新菜单。
      resolveFirst([{ path: 'stale.ts' }]);
      await Promise.resolve();
      await Promise.resolve();
      expect(menu.textContent).not.toContain('stale.ts');

      // 之后再让第二次请求跑完，新候选应正常出现。
      await vi.advanceTimersByTimeAsync(300);
      expect(menu.textContent).toContain('new.ts');
      completer.hide();
      input.remove(); menu.remove();
    } finally {
      vi.useRealTimers();
    }
  });
});
