import { describe, expect, it, vi } from 'vitest';
import { detect, apply } from '@/modules/mention';

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
