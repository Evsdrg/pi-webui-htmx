export function readPreference(key: string): string { try { return localStorage.getItem(`pi-ui:${key}`) ?? ''; } catch { return ''; } }
export function savePreference(key: string, value: string): void { try { localStorage.setItem(`pi-ui:${key}`, value); } catch { /* 禁用存储时保持当前页面可用。 */ } }
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
  // 主题接线按需加载：首屏防闪烁由 shell.html 内联脚本完成，交互逻辑
  // 放独立分块，不占首屏预算（与 branch/models 同一策略）。
  void import('./theme').then(({ mountTheme }) => { if (!abort.signal.aborted) mountTheme(abort.signal); });
  // 对话字号与行宽：两个滑块写的是 .conversation 上的 CSS 变量，
  // 数值标签同步显示，让用户知道当前是哪一档。
  const conv = document.getElementById('main')!;
  const font = document.getElementById('chat-font-size') as HTMLInputElement | null;
  const width = document.getElementById('chat-width') as HTMLInputElement | null;
  const widthFull = document.getElementById('chat-width-full') as HTMLInputElement | null;
  const syncChat = () => {
    if (font) { const offset = Number(font.value) || 0; conv.style.setProperty('--chat-font-offset', `${offset}px`); const label = document.getElementById('chat-font-size-value'); if (label) label.textContent = offset === 0 ? '（默认）' : `${offset > 0 ? '+' : ''}${offset}px`; }
    if (width) {
      // 顶满不是另一档像素值，而是解除行宽限制：变量给 100%，
      // 消息区/输入区的居中内边距（max(20px, …)）自动退到最小呼吸边距。
      const full = widthFull?.checked === true;
      const w = Number(width.value) || 820;
      conv.style.setProperty('--chat-width', full ? '100%' : `${w}px`);
      width.disabled = full;
      const label = document.getElementById('chat-width-value'); if (label) label.textContent = full ? '顶满' : `${w}px`;
    }
  };
  // 用 || 而不是 ??：readPreference 在没有存过时返回空串，不是 null。
  // 空串赋给 range 会让浏览器自己挑一个默认值（规格上是 min 与 max 的中点），
  // 于是「从未设置过」的用户一进来就看到 +1px 这类莫名其妙的偏移。
  if (font) { font.value = readPreference('chat-font-offset') || '0'; font.addEventListener('input', () => { syncChat(); savePreference('chat-font-offset', font.value); }, { signal: abort.signal }); }
  if (width) { width.value = readPreference('chat-width') || '820'; width.addEventListener('input', () => { if (widthFull?.checked) return; syncChat(); savePreference('chat-width', width.value); }, { signal: abort.signal }); }
  if (widthFull) {
    widthFull.checked = readPreference('chat-width-full') === '1';
    widthFull.addEventListener('change', () => { savePreference('chat-width-full', widthFull.checked ? '1' : '0'); syncChat(); }, { signal: abort.signal });
  }
  syncChat();
  const collapsed = document.getElementById('process-collapsed') as HTMLInputElement | null;
  let processCollapsed = readPreference('process-collapsed') !== '0';
  const initialized = new WeakSet<HTMLDetailsElement>();
  const syncProcess = (force = false) => {
    for (const group of document.querySelectorAll<HTMLDetailsElement>('#turns details.turn-process')) {
      if (!force && initialized.has(group)) continue;
      group.open = !processCollapsed;
      initialized.add(group);
    }
  };
  if (collapsed) {
    collapsed.checked = processCollapsed;
    collapsed.addEventListener('change', () => {
      processCollapsed = collapsed.checked;
      savePreference('process-collapsed', processCollapsed ? '1' : '0');
      syncProcess(true);
    }, { signal: abort.signal });
  }
  syncProcess();
  document.body.addEventListener('htmx:afterSwap', () => syncProcess(), { signal: abort.signal });
  // 会话列表视图（时间线 / 按工作区）记忆。
  //
  // 选中态与携带的 view 值都由这里从偏好推导，控件本身不保存状态：
  // 用按钮而不是 select，正是为了避开浏览器在 reload 时对表单控件的值恢复——
  // 恢复出来的值可能与偏好无关，控件显示与列表内容就会对不上（真机复现过）。
  // 片段里的 OOB 会整块换掉这段控件（例如从分组视图点「查看全部」切回时间线），
  // 所以换入之后要再读一次服务端给的值写回偏好，避免两边各说各话。
  const viewSwitch = document.getElementById('view-switch');
  if (viewSwitch) {
    const syncView = (value?: string) => {
      const view = value ?? readPreference('session-view');
      const want = view === 'workspace' ? 'workspace' : 'timeline';
      const buttons = viewSwitch.querySelectorAll<HTMLButtonElement>('.view-btn');
      buttons.forEach((button) => button.setAttribute('aria-pressed', String(button.dataset.view === want)));
      const carrier = document.getElementById('session-view-value') as HTMLInputElement | null;
      if (carrier) carrier.value = want;
      savePreference('session-view', want);
      return want;
    };
    const saved = syncView();
    // 偏好不是默认视图时先按它加载一次，否则页面会先渲染时间线再跳成分组。
    if (saved === 'workspace') window.htmx.trigger(document.body, 'sessions-refresh');
    viewSwitch.addEventListener('click', (event) => {
      const button = (event.target as Element).closest<HTMLElement>('.view-btn');
      if (button) syncView(button.dataset.view);
    }, { signal: abort.signal });
    document.body.addEventListener('htmx:afterSwap', () => {
      // 服务端换入的整块控件已经带上 aria-pressed，这里只把偏好对齐。
      const carrier = document.getElementById('session-view-value') as HTMLInputElement | null;
      if (carrier) savePreference('session-view', carrier.value === 'workspace' ? 'workspace' : 'timeline');
    }, { signal: abort.signal });
  }
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
  resize.addEventListener('lostpointercapture', end, { signal: abort.signal });
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
    const button = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-action=sidebar],[data-close-dialog]') : null;
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
  let observer: ResizeObserver | undefined;
  const topBar = document.querySelector<HTMLElement>('.topbar');
  if (topBar) {
    const sync = () => document.querySelector<HTMLElement>('.conversation')?.style.setProperty('--topbar-h', `${topBar.getBoundingClientRect().height}px`);
    sync();
    if (typeof ResizeObserver === 'function') { observer = new ResizeObserver(sync); observer.observe(topBar); }
    window.addEventListener('resize', sync, { signal: abort.signal });
  }
  return () => { observer?.disconnect(); abort.abort(); };
}
export function closeMobileSidebar(): void {
  const shell = document.getElementById('workbench'); if (shell) shell.dataset.mobileSidebar = 'closed';
  const backdrop = document.querySelector<HTMLElement>('.sidebar-backdrop'); if (backdrop) backdrop.hidden = true;
  if (window.matchMedia?.('(max-width:760px)').matches) document.querySelector<HTMLElement>('[aria-controls="sidebar"]')?.setAttribute('aria-expanded', 'false');
}
