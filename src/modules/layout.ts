export const THEMES = ['system', 'light', 'dark', 'mist', 'rose', 'pine'] as const;
export function readPreference(key: string): string { try { return localStorage.getItem(`pi-ui:${key}`) ?? ''; } catch { return ''; } }
export function savePreference(key: string, value: string): void { try { localStorage.setItem(`pi-ui:${key}`, value); } catch { /* 禁用存储时保持当前页面可用。 */ } }
export function applyTheme(theme: string): void {
  const value = THEMES.includes(theme as typeof THEMES[number]) ? theme : 'system';
  if (value === 'system') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = value;
  savePreference('theme', value);
}
export function clampSidebar(width: number): number { return Number.isFinite(width) ? Math.min(380, Math.max(200, width)) : 260; }
type Draft = { key: string; text: string };
function drafts(): Draft[] {
  try { const value = JSON.parse(readPreference('drafts') || '[]') as unknown; return Array.isArray(value) ? value.filter((v): v is Draft => !!v && typeof v === 'object' && typeof v.key === 'string' && typeof v.text === 'string').slice(0, 8) : []; } catch { return []; }
}
export function readDraft(key: string): string { return drafts().find((d) => d.key === key)?.text ?? ''; }
export function saveDraft(key: string, text: string): void {
  const next = drafts().filter((d) => d.key !== key);
  if (text) next.unshift({ key, text: text.slice(0, 20_000) });
  savePreference('drafts', JSON.stringify(next.slice(0, 8)));
}
export function mountLayout(): () => void {
  const abort = new AbortController();
  const shell = document.getElementById('workbench')!;
  const theme = document.getElementById('theme-select') as HTMLSelectElement;
  theme.value = readPreference('theme') || 'system'; applyTheme(theme.value);
  theme.addEventListener('change', () => applyTheme(theme.value), { signal: abort.signal });
  const resize = document.querySelector<HTMLElement>('.sidebar-resizer')!;
  let drag: { x: number; width: number; pointer: number } | null = null;
  const setWidth = (width: number) => { const w = clampSidebar(width); shell.style.setProperty('--sidebar-width', `${w}px`); resize.setAttribute('aria-valuenow', String(w)); return w; };
  setWidth(Number(readPreference('sidebar-width')) || 260);
  resize.setAttribute('aria-valuemin', '200'); resize.setAttribute('aria-valuemax', '380');
  resize.addEventListener('pointerdown', (event) => { drag = { x: event.clientX, width: document.getElementById('sidebar')!.getBoundingClientRect().width, pointer: event.pointerId }; resize.setPointerCapture(event.pointerId); shell.setAttribute('data-resizing', ''); }, { signal: abort.signal });
  resize.addEventListener('pointermove', (event) => { if (drag) setWidth(drag.width + event.clientX - drag.x); }, { signal: abort.signal });
  // 拖动期间关掉容器宽度过渡：否则每次 pointermove 都在追一个正在动画的目标值，手感发黏。
  const end = () => { if (!drag) return; drag = null; shell.removeAttribute('data-resizing'); savePreference('sidebar-width', resize.getAttribute('aria-valuenow') ?? '260'); };
  resize.addEventListener('pointerup', end, { signal: abort.signal });
  resize.addEventListener('pointercancel', end, { signal: abort.signal });
  resize.addEventListener('keydown', (event) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return; event.preventDefault();
    savePreference('sidebar-width', String(setWidth(Number(resize.getAttribute('aria-valuenow')) + (event.key === 'ArrowLeft' ? -10 : 10))));
  }, { signal: abort.signal });
  const toggle = document.querySelector<HTMLElement>('[aria-controls="sidebar"]');
  const syncExpanded = () => toggle?.setAttribute('aria-expanded', String(matchMedia('(max-width:760px)').matches
    ? shell.dataset.mobileSidebar === 'open' : shell.dataset.sidebar !== 'closed'));
  syncExpanded();
  window.addEventListener('resize', syncExpanded, { signal: abort.signal });
  document.addEventListener('click', (event) => {
    const button = (event.target as Element).closest<HTMLElement>('[data-action=sidebar],[data-close-dialog]');
    if (!button) return;
    if (button.hasAttribute('data-close-dialog')) { button.closest('dialog')?.close(); return; }
    if (matchMedia('(max-width:760px)').matches) {
      const open = shell.dataset.mobileSidebar !== 'open'; shell.dataset.mobileSidebar = open ? 'open' : 'closed';
      (document.querySelector('.sidebar-backdrop') as HTMLElement).hidden = !open;
    } else shell.dataset.sidebar = shell.dataset.sidebar === 'closed' ? 'open' : 'closed';
    syncExpanded();
  }, { signal: abort.signal });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeMobileSidebar(); }, { signal: abort.signal });
  // 顶栏高度同步给 --topbar-h：顶栏面板是贴顶栏下沿的下拉浮层，
  // 需要知道它有多高。CSS 里有断点默认值，这里量真实值兜底——
  // 状态文字变长、按钮换行都会让实际高度偏离默认值。
  const topBar = document.querySelector<HTMLElement>('.topbar');
  if (topBar) {
    const sync = () => document.querySelector<HTMLElement>('.conversation')?.style.setProperty('--topbar-h', `${topBar.getBoundingClientRect().height}px`);
    sync();
    if (typeof ResizeObserver === 'function') new ResizeObserver(sync).observe(topBar);
    window.addEventListener('resize', sync, { signal: abort.signal });
  }
  return () => abort.abort();
}
export function closeMobileSidebar(): void {
  const shell = document.getElementById('workbench'); if (shell) shell.dataset.mobileSidebar = 'closed';
  const backdrop = document.querySelector<HTMLElement>('.sidebar-backdrop'); if (backdrop) backdrop.hidden = true;
  if (window.matchMedia?.('(max-width:760px)').matches) document.querySelector<HTMLElement>('[aria-controls="sidebar"]')?.setAttribute('aria-expanded', 'false');
}
