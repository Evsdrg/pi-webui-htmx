import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { openFullHistory, toggle, type TopbarHost } from '@/modules/topbar';

const shell = readFileSync(resolve(process.cwd(), 'src/templates/shell.html'), 'utf8');
const topbar = shell.slice(shell.indexOf('<header class="topbar">'), shell.indexOf('<div id="connection-notice"'));
const panels = ['panel-info', 'panel-title', 'panel-system', 'panel-tools', 'panel-mc', 'panel-goal'];
const host: TopbarHost = {
  sessionId: () => '', cwd: () => '', busy: () => false, modelLabel: () => '', contextWindow: () => 0,
  setContextWindow() {}, thinking: () => '', preset: () => 'default', setPreset() {}, presetKey: () => '',
  presetBySession: () => new Map(), setTitle() {}, request: vi.fn(async () => ({})) as TopbarHost['request'],
  notify: vi.fn(), fail: vi.fn(), refreshState: async () => {},
};
const button = (action: string) => document.querySelector<HTMLButtonElement>(`.topbar [data-action="${action}"]`)!;
const expanded = () => [...document.querySelectorAll<HTMLElement>('.topbar [aria-expanded="true"][data-action^="panel-"]')].map(node => node.dataset.action);

beforeEach(() => {
  document.body.innerHTML = topbar;
  vi.stubGlobal('htmx', { trigger: vi.fn() });
});
afterEach(() => { document.body.replaceChildren(); vi.unstubAllGlobals(); });

describe('真实顶部模板的面板按钮选中态', () => {
  it('完整历史不声明标题面板的控制和展开属性', () => {
    expect(button('full-history').hasAttribute('aria-controls')).toBe(false);
    expect(button('full-history').hasAttribute('aria-expanded')).toBe(false);
  });

  it('生成标题打开时仅自身选中，再点或显式关闭均复位', () => {
    toggle(host, 'panel-title');
    expect(document.getElementById('panel-title')!.hidden).toBe(false);
    expect(button('panel-title').getAttribute('aria-expanded')).toBe('true');
    expect(button('full-history').getAttribute('aria-expanded')).not.toBe('true');
    expect(expanded()).toEqual(['panel-title']);
    toggle(host, 'panel-title');
    expect(document.getElementById('panel-title')!.hidden).toBe(true);
    expect(expanded()).toEqual([]);
    toggle(host, 'panel-title');
    toggle(host, 'panel-title', false);
    expect(expanded()).toEqual([]);
  });

  it.each(panels)('切到 %s 时只保留对应面板和按钮状态', target => {
    toggle(host, 'panel-title');
    toggle(host, target, true);
    expect(expanded()).toEqual([target]);
    for (const id of panels) expect(document.getElementById(id)!.hidden).toBe(id !== target);
    expect(button('sidebar').getAttribute('aria-expanded')).toBe('true');
    expect(button('workspace').getAttribute('aria-expanded')).toBe('false');
    expect(button('full-history').getAttribute('aria-expanded')).not.toBe('true');
  });

  it('无关按钮误带相同控制目标时也不能抢走面板选中态', () => {
    button('full-history').setAttribute('aria-controls', 'panel-title');
    button('full-history').setAttribute('aria-expanded', 'false');
    toggle(host, 'panel-title');
    expect(button('panel-title').getAttribute('aria-expanded')).toBe('true');
    expect(button('full-history').getAttribute('aria-expanded')).toBe('false');
    toggle(host, 'panel-system');
    expect(button('panel-title').getAttribute('aria-expanded')).toBe('false');
    expect(expanded()).toEqual(['panel-system']);
  });

  it('完整历史操作不改变当前面板的选中态', async () => {
    toggle(host, 'panel-title');
    await openFullHistory(host);
    expect(host.notify).toHaveBeenCalled();
    expect(expanded()).toEqual(['panel-title']);
    expect(button('full-history').getAttribute('aria-expanded')).not.toBe('true');
  });
});
