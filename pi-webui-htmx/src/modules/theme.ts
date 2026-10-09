// 主题解析与接线：显示模式（跟随系统/白天/夜间）× 两侧各自的调色板。
// 独立成模块并按需 import：首屏的防闪烁由 shell.html 的内联脚本完成，
// 这里只负责交互与控件状态，不值得占用首屏预算。
//
// 解析结果写进 <html>：data-theme=生效的调色板（tokens.css 按它选值），
// data-mode=明暗（app.css 的 color-scheme 用它）。换主题只有一个入口，
// tokens.css 里不再有媒体查询深色块（曾靠它实现「跟随系统」，两处入口
// 必须逐行一致才不分叉；现在由这里的解析单点负责）。
export const LIGHT_THEMES = ['light', 'mist', 'rose', 'pine'];
export const DARK_THEMES = ['dark', 'obsidian', 'jade', 'plum'];
export type ThemeMode = 'system' | 'light' | 'dark';
export interface ThemeState { mode: ThemeMode; light: string; dark: string }
function read(key: string): string { try { return localStorage.getItem(`pi-ui:${key}`) ?? ''; } catch { return ''; } }
function save(key: string, value: string): void { try { localStorage.setItem(`pi-ui:${key}`, value); } catch { /* 禁用存储时保持当前页面可用。 */ } }
/** 读偏好并规范化：无效值一律回退默认（跟随系统 + 明亮 + 深夜蓝）。 */
export function readTheme(): ThemeState {
  const mode = read('theme-mode');
  const light = read('theme-light');
  const dark = read('theme-dark');
  return {
    mode: mode === 'light' || mode === 'dark' ? mode : 'system',
    light: LIGHT_THEMES.includes(light) ? light : 'light',
    dark: DARK_THEMES.includes(dark) ? dark : 'dark',
  };
}
/** 当前是否应呈现为夜间（system 模式看系统偏好）。 */
export function isDarkResolved(state: ThemeState = readTheme()): boolean {
  return state.mode === 'dark' || (state.mode === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
}
/** 应用主题并返回生效的调色板 id。 */
export function applyTheme(state: ThemeState = readTheme()): string {
  const dark = isDarkResolved(state);
  const palette = dark ? state.dark : state.light;
  const root = document.documentElement;
  root.dataset.theme = palette;
  root.dataset.mode = dark ? 'dark' : 'light';
  return palette;
}
function saveMode(mode: ThemeMode): void { save('theme-mode', mode); }
function savePalette(palette: string): void {
  save(LIGHT_THEMES.includes(palette) ? 'theme-light' : 'theme-dark', palette);
}

// mountTheme 接线主题控件：设置里的模式/两侧调色板、顶栏快捷切换。
// 全部走一个 renderTheme 重读偏好再应用，控件状态（aria-pressed/图标）
// 从同一份状态推导，避免「显示的选择与实际生效的主题」两处各说各话。
export function mountTheme(signal: AbortSignal): void {
  const toggleIcon = document.getElementById('theme-toggle-icon');
  const renderTheme = (): void => {
    const state = readTheme();
    const dark = isDarkResolved(state);
    applyTheme(state);
    for (const button of document.querySelectorAll<HTMLElement>('[data-theme-mode]')) {
      button.setAttribute('aria-pressed', String(button.dataset.themeMode === state.mode));
    }
    for (const button of document.querySelectorAll<HTMLElement>('[data-theme-pick]')) {
      const pick = button.dataset.themePick ?? '';
      button.setAttribute('aria-pressed', String(pick === (LIGHT_THEMES.includes(pick) ? state.light : state.dark)));
    }
    toggleIcon?.setAttribute('href', dark ? '#icon-moon' : '#icon-sun');
  };
  renderTheme();
  // system 模式跟随系统切换：只有系统偏好变化且模式仍是 system 才重解析。
  // 可选调用兼容测试桩与较老的实现（可能只提供 matches，没有监听）。
  matchMedia('(prefers-color-scheme: dark)').addEventListener?.('change', () => {
    if (readTheme().mode === 'system') renderTheme();
  }, { signal });
  document.addEventListener('click', (event) => {
    const target = event.target as Element;
    if (target.closest('[data-action=theme-toggle]')) { saveMode(isDarkResolved() ? 'light' : 'dark'); renderTheme(); return; }
    const mode = target.closest<HTMLElement>('[data-theme-mode]');
    if (mode) { saveMode((mode.dataset.themeMode ?? 'system') as ThemeMode); renderTheme(); return; }
    const pick = target.closest<HTMLElement>('[data-theme-pick]');
    if (pick?.dataset.themePick) { savePalette(pick.dataset.themePick); renderTheme(); }
  }, { signal });
}
