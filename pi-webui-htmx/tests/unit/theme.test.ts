// 首帧内联脚本与运行时分别解析偏好，测试必须覆盖两者一致性。
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import vm from 'node:vm';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DARK_THEMES, LIGHT_THEMES, applyTheme, mountTheme, readTheme } from '@/modules/theme';

// jsdom 下 import.meta.url 不是 file 方案，用项目根（vitest 的 cwd）定位模板。
const SHELL = readFileSync(resolve(process.cwd(), 'src/templates/shell.html'), 'utf8');
const HEAD = SHELL.slice(0, SHELL.indexOf('</head>'));
// head 里第一个内联 <script> 就是防闪烁脚本（带 src 的入口脚本在 </body> 前）。
const FIRST_FRAME = /<script>([\s\S]*?)<\/script>/.exec(HEAD)?.[1] ?? '';

// 用 node:vm 在隔离上下文里执行抽出来的首帧脚本：它自己的 localStorage /
// matchMedia / document 都注入沙箱，不污染 jsdom 全局，也不用 eval。
function runFirstFrame(prefs: Record<string, string>, prefersDark: boolean): Record<string, string> {
  const store = new Map(Object.entries(prefs));
  const sandbox = {
    localStorage: { getItem: (key: string) => (store.has(key) ? store.get(key)! : null) },
    matchMedia: () => ({ matches: prefersDark }),
    document: { documentElement: { dataset: {} as Record<string, string> } },
  };
  vm.runInNewContext(FIRST_FRAME, sandbox);
  return sandbox.document.documentElement.dataset;
}

// 与首帧脚本同样的输入走 theme.ts 的解析，用于比对两处结果。
function applyFromPrefs(prefs: Record<string, string>, prefersDark: boolean): Record<string, string> {
  localStorage.clear();
  for (const [key, value] of Object.entries(prefs)) localStorage.setItem(key, value);
  stubPrefersDark(prefersDark);
  applyTheme();
  return {
    theme: document.documentElement.dataset.theme ?? '',
    mode: document.documentElement.dataset.mode ?? '',
  };
}

type PrefersStub = { matches: boolean; listeners: Array<() => void> };
let mql: PrefersStub;
// 测试桩返回一个带 addEventListener 的 MediaQueryList 影子：主题模块靠它
// 监听系统明暗变化（较老实现只有 matches，因此模块用可选调用）。
function stubPrefersDark(matches: boolean): PrefersStub {
  mql = { matches, listeners: [] };
  vi.stubGlobal('matchMedia', () => ({
    get matches() { return mql.matches; },
    addEventListener: (_type: string, listener: () => void) => { mql.listeners.push(listener); },
  }));
  return mql;
}
function fireSystemChange(matches: boolean): void {
  mql.matches = matches;
  for (const listener of mql.listeners) listener();
}

let abort: AbortController | undefined;
function mount(): void {
  abort = new AbortController();
  mountTheme(abort.signal);
}
function fixture(): void {
  document.body.innerHTML = `
    <button data-action="theme-toggle" id="theme-toggle"><svg><use id="theme-toggle-icon" href="#icon-sun"/></svg></button>
    <button data-theme-mode="system">跟随系统</button>
    <button data-theme-mode="light">白天</button>
    <button data-theme-mode="dark">夜间</button>
    <button data-theme-pick="light">明亮</button>
    <button data-theme-pick="mist">雾蓝</button>
    <button data-theme-pick="rose">蔷薇</button>
    <button data-theme-pick="pine">松绿</button>
    <button data-theme-pick="dark">深夜蓝</button>
    <button data-theme-pick="obsidian">纯黑</button>
    <button data-theme-pick="jade">墨绿</button>
    <button data-theme-pick="plum">暮紫</button>`;
}
function click(selector: string): void {
  document.querySelector<HTMLElement>(selector)!.click();
}
const rootTheme = () => document.documentElement.dataset.theme;
const rootMode = () => document.documentElement.dataset.mode;

beforeEach(() => {
  localStorage.clear();
  stubPrefersDark(false);
  delete document.documentElement.dataset.theme;
  delete document.documentElement.dataset.mode;
});
afterEach(() => {
  abort?.abort();
  abort = undefined;
  document.body.replaceChildren();
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.removeAttribute('data-mode');
  vi.unstubAllGlobals();
});

describe('主题枚举与解析', () => {
  it('白天与夜间各 4 个调色板：新增 jade/plum，保留 dark/obsidian', () => {
    expect([...LIGHT_THEMES]).toEqual(['light', 'mist', 'rose', 'pine']);
    expect([...DARK_THEMES]).toEqual(['dark', 'obsidian', 'jade', 'plum']);
    expect(LIGHT_THEMES).toHaveLength(4);
    expect(DARK_THEMES).toHaveLength(4);
  });

  it('readTheme 接受合法的四对调色板，两侧各记各的', () => {
    localStorage.setItem('pi-ui:theme-light', 'pine');
    localStorage.setItem('pi-ui:theme-dark', 'jade');
    expect(readTheme()).toEqual({ mode: 'system', light: 'pine', dark: 'jade' });
    localStorage.setItem('pi-ui:theme-dark', 'plum');
    expect(readTheme()).toEqual({ mode: 'system', light: 'pine', dark: 'plum' });
  });

  it('非法调色板与模式一律回退默认', () => {
    localStorage.setItem('pi-ui:theme-light', 'bogus');
    localStorage.setItem('pi-ui:theme-dark', 'midnight');
    localStorage.setItem('pi-ui:theme-mode', 'purple');
    expect(readTheme()).toEqual({ mode: 'system', light: 'light', dark: 'dark' });
  });
});

describe('顶栏切换保留用户选定的夜间调色板', () => {
  it('白天点一下进入用户选的 jade（不是硬编码 dark）', () => {
    localStorage.setItem('pi-ui:theme-light', 'light');
    localStorage.setItem('pi-ui:theme-dark', 'jade');
    localStorage.setItem('pi-ui:theme-mode', 'light');
    fixture(); mount();
    expect(rootTheme()).toBe('light');
    click('#theme-toggle');
    expect(localStorage.getItem('pi-ui:theme-mode')).toBe('dark');
    expect(rootTheme()).toBe('jade');
    expect(rootMode()).toBe('dark');
  });

  it('白天点一下进入用户选的 plum', () => {
    localStorage.setItem('pi-ui:theme-light', 'mist');
    localStorage.setItem('pi-ui:theme-dark', 'plum');
    localStorage.setItem('pi-ui:theme-mode', 'light');
    fixture(); mount();
    click('#theme-toggle');
    expect(rootTheme()).toBe('plum');
    expect(rootMode()).toBe('dark');
  });

  it('夜间点一下回到用户选的白天调色板', () => {
    localStorage.setItem('pi-ui:theme-light', 'pine');
    localStorage.setItem('pi-ui:theme-dark', 'plum');
    localStorage.setItem('pi-ui:theme-mode', 'dark');
    fixture(); mount();
    expect(rootTheme()).toBe('plum');
    click('#theme-toggle');
    expect(localStorage.getItem('pi-ui:theme-mode')).toBe('light');
    expect(rootTheme()).toBe('pine');
    expect(rootMode()).toBe('light');
  });
});

describe('系统偏好与显式模式', () => {
  it('system 模式下系统明暗变化重解析到对应用户调色板', () => {
    localStorage.setItem('pi-ui:theme-light', 'mist');
    localStorage.setItem('pi-ui:theme-dark', 'jade');
    // 未写 theme-mode → system
    stubPrefersDark(false);
    fixture(); mount();
    expect(rootTheme()).toBe('mist');
    fireSystemChange(true);
    expect(rootTheme()).toBe('jade');
    expect(rootMode()).toBe('dark');
    fireSystemChange(false);
    expect(rootTheme()).toBe('mist');
  });

  it('显式 light/dark 不随系统偏好变化', () => {
    localStorage.setItem('pi-ui:theme-light', 'rose');
    localStorage.setItem('pi-ui:theme-dark', 'jade');
    localStorage.setItem('pi-ui:theme-mode', 'light');
    stubPrefersDark(false);
    fixture(); mount();
    expect(rootTheme()).toBe('rose');
    fireSystemChange(true);
    expect(rootTheme()).toBe('rose');
    expect(rootMode()).toBe('light');
    // 显式 light 下点一下仍进入夜间侧的用户调色板。
    click('#theme-toggle');
    expect(rootTheme()).toBe('jade');
    expect(rootMode()).toBe('dark');
  });
});

describe('两侧调色板各记各的', () => {
  it('选白天调色板只写 theme-light，选夜间只写 theme-dark', () => {
    fixture(); mount();
    click('[data-theme-pick="mist"]');
    expect(localStorage.getItem('pi-ui:theme-light')).toBe('mist');
    expect(localStorage.getItem('pi-ui:theme-dark')).toBeNull();
    click('[data-theme-pick="jade"]');
    expect(localStorage.getItem('pi-ui:theme-dark')).toBe('jade');
    expect(localStorage.getItem('pi-ui:theme-light')).toBe('mist');
    expect(readTheme()).toEqual({ mode: 'system', light: 'mist', dark: 'jade' });
  });

  it('模式按钮写入 theme-mode', () => {
    fixture(); mount();
    click('[data-theme-mode="dark"]');
    expect(localStorage.getItem('pi-ui:theme-mode')).toBe('dark');
    click('[data-theme-mode="system"]');
    expect(localStorage.getItem('pi-ui:theme-mode')).toBe('system');
  });
});

describe('shell.html 首帧内联脚本与 theme.ts 一致', () => {
  it('能抽出 head 首个内联脚本，且明暗清单与 theme.ts 相同', () => {
    expect(FIRST_FRAME).toContain('prefers-color-scheme');
    // 注意 D 声明为「var L=[...],D=[...]」——D 前是逗号不是 var，故不能锚 var。
    const arr = (name: string): string[] =>
      [...new RegExp(`${name}=\\[([^\\]]*)\\]`).exec(FIRST_FRAME)![1].matchAll(/'([a-z-]+)'/g)].map((m) => m[1]);
    expect(arr('L')).toEqual([...LIGHT_THEMES]);
    expect(arr('D')).toEqual([...DARK_THEMES]);
  });

  it.each<{ prefs: Record<string, string>; prefersDark: boolean; theme: string; mode: string }>([
    { prefs: { 'pi-ui:theme-mode': 'dark', 'pi-ui:theme-dark': 'jade' }, prefersDark: false, theme: 'jade', mode: 'dark' },
    { prefs: { 'pi-ui:theme-mode': 'system', 'pi-ui:theme-dark': 'plum' }, prefersDark: true, theme: 'plum', mode: 'dark' },
    { prefs: { 'pi-ui:theme-mode': 'system', 'pi-ui:theme-dark': 'plum' }, prefersDark: false, theme: 'light', mode: 'light' },
    { prefs: { 'pi-ui:theme-mode': 'bogus', 'pi-ui:theme-dark': 'midnight' }, prefersDark: true, theme: 'dark', mode: 'dark' },
    { prefs: { 'pi-ui:theme-mode': 'light', 'pi-ui:theme-light': 'pine' }, prefersDark: true, theme: 'pine', mode: 'light' },
  ])('首帧脚本与 applyTheme 结果一致（$theme/$mode）', ({ prefs, prefersDark, theme, mode }) => {
    const frame = runFirstFrame(prefs, prefersDark);
    const applied = applyFromPrefs(prefs, prefersDark);
    expect(frame).toEqual({ theme, mode });
    expect(applied).toEqual({ theme, mode });
    expect(frame).toEqual(applied);
  });
});

describe('shell.html 夜间主题按钮与枚举一致', () => {
  function pickValues(label: string): string[] {
    const segment = new RegExp(`aria-label="${label}"[^>]*>([\\s\\S]*?)</div>`).exec(SHELL)?.[1] ?? '';
    return [...segment.matchAll(/data-theme-pick="([a-z-]+)"/g)].map((m) => m[1]);
  }
  function groupSegment(label: string): string {
    return new RegExp(`aria-label="${label}"[^>]*>([\\s\\S]*?)</div>`).exec(SHELL)?.[1] ?? '';
  }

  it('白天/夜间按钮集合与 LIGHT/DARK 一一对应', () => {
    expect(pickValues('白天主题')).toEqual([...LIGHT_THEMES]);
    expect(pickValues('夜间主题')).toEqual([...DARK_THEMES]);
  });

  it('夜间新增按钮用中文名 墨绿/暮紫', () => {
    expect(groupSegment('夜间主题')).toMatch(/data-theme-pick="jade"[^>]*>墨绿</);
    expect(groupSegment('夜间主题')).toMatch(/data-theme-pick="plum"[^>]*>暮紫</);
  });

  it('切换按钮与三种模式按钮齐全', () => {
    expect(SHELL).toContain('data-action="theme-toggle"');
    expect([...SHELL.matchAll(/data-theme-mode="([a-z]+)"/g)].map((m) => m[1])).toEqual(['system', 'light', 'dark']);
  });
});
