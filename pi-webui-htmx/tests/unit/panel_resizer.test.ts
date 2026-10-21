// 右侧工作区面板的宽度拖动。
//
// 左侧栏一直可拖（.sidebar-resizer），右侧面板只能接受固定 390px——文件
// 预览和终端都要在这个宽度里挤，长行代码与终端输出明显不够看。这里补上
// 同一个交互：拖动条在 main 与面板之间，宽度写进 --panel-width，偏好持久化。
//
// 与左侧拖动的两处有意差别：
//   - 上限不是固定值，而是「主区至少还剩多少」。屏幕宽度差异很大，
//     写死 380 在 4K 屏上会显得没拖开。
//   - 仅在面板打开时可拖；关闭时拖动条不占位（否则 grid 多出一列空白）。
import { afterEach, expect, test, vi } from 'vitest';
import { mountLayout } from '@/modules/layout';

let dispose: (() => void) | undefined;

function rect(width: number, height = 800): DOMRect {
  return { width, height, top: 0, bottom: height, left: 0, right: width, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
}

const VIEWPORT_W = 1600;
const PANEL_W0 = 390;
// 主区不得窄于这个值：再窄就无法读对话了。
const MIN_MAIN_W = 420;

function mount(options: { open?: boolean; savedWidth?: string } = {}): { shell: HTMLElement; resizer: HTMLElement; panel: HTMLElement } {
  const open = options.open !== false;
  document.body.innerHTML = `
    <div id="workbench" data-right-panel="${open ? 'open' : 'closed'}">
      <aside id="sidebar"></aside>
      <div class="sidebar-resizer"></div>
      <main class="conversation" id="main"></main>
      <div class="panel-resizer" role="separator" aria-label="调整面板宽度" aria-orientation="vertical" tabindex="0"></div>
      <aside id="workspace-panel"${open ? '' : ' hidden'}></aside>
    </div>`;
  if (options.savedWidth !== undefined) localStorage.setItem('pi-ui:panel-width', options.savedWidth);
  else localStorage.removeItem('pi-ui:panel-width');
  vi.stubGlobal('innerWidth', VIEWPORT_W);
  vi.stubGlobal('htmx', { ajax: vi.fn().mockResolvedValue(undefined), trigger: vi.fn() });
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { callback(0); return 1; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
  Object.defineProperty(Element.prototype, 'setPointerCapture', { configurable: true, value: vi.fn() });
  Object.defineProperty(Element.prototype, 'releasePointerCapture', { configurable: true, value: vi.fn() });
  // 打桩必须在 mountLayout 之前：它在初始化时就要读一次容器宽度来夹
  // 恢复的偏好，晚一步打桩就会按 jsdom 的 0 宽度把值夹到下限。
  const shell = document.getElementById('workbench')!;
  vi.spyOn(shell, 'getBoundingClientRect').mockReturnValue(rect(VIEWPORT_W));
  // 拖动条的逻辑挂在 mountLayout 里（与左侧拖动条同一处），
  // 不是 Workspace —— 后者只管文件树的纵向分隔条。
  dispose = mountLayout();
  return { shell, resizer: document.querySelector('.panel-resizer')!, panel: document.getElementById('workspace-panel')! };
}

const pointer = (type: string, clientX: number) => new PointerEvent(type, { clientX, pointerId: 1, bubbles: true });
const widthOf = (shell: HTMLElement) => Number.parseFloat(shell.style.getPropertyValue('--panel-width'));

afterEach(() => { dispose?.(); dispose = undefined; vi.unstubAllGlobals(); document.body.replaceChildren(); });

test('拖动条把面板调宽并持久化', () => {
  const { shell, resizer, panel } = mount();
  // 面板在右侧：向左拖 → 变宽。
  resizer.dispatchEvent(pointer('pointerdown', 1200));
  resizer.dispatchEvent(pointer('pointermove', 1100));
  resizer.dispatchEvent(pointer('pointerup', 1100));
  expect(widthOf(shell)).toBe(PANEL_W0 + 100);
  expect(shell.style.getPropertyValue('--panel-width')).toBe('490px');
  expect(localStorage.getItem('pi-ui:panel-width')).toBe('490');
  expect(panel.hidden).toBe(false);
});

test('宽度被夹在可达范围内', () => {
  const { shell, resizer } = mount();
  // 向右拖到负宽度。
  resizer.dispatchEvent(pointer('pointerdown', 1200));
  resizer.dispatchEvent(pointer('pointermove', 3000));
  resizer.dispatchEvent(pointer('pointerup', 3000));
  expect(widthOf(shell)).toBeGreaterThan(0);
  // 向左拖到远超屏幕。
  resizer.dispatchEvent(pointer('pointerdown', 1200));
  resizer.dispatchEvent(pointer('pointermove', -5000));
  resizer.dispatchEvent(pointer('pointerup', -5000));
  expect(widthOf(shell)).toBeLessThanOrEqual(VIEWPORT_W - MIN_MAIN_W);
});

test('刷新后按保存的宽度恢复', () => {
  const { shell } = mount({ savedWidth: '520' });
  expect(widthOf(shell)).toBe(520);
  expect(shell.style.getPropertyValue('--panel-width')).toBe('520px');
});

test('保存的宽度超出新屏幕时按当前视口夹回去', () => {
  const { shell } = mount({ savedWidth: '3000' });
  expect(widthOf(shell)).toBeLessThanOrEqual(VIEWPORT_W - MIN_MAIN_W);
});

test('面板关闭时拖动不改变宽度', () => {
  const { shell, resizer } = mount({ open: false });
  const before = shell.style.getPropertyValue('--panel-width');
  resizer.dispatchEvent(pointer('pointerdown', 1200));
  resizer.dispatchEvent(pointer('pointermove', 1100));
  resizer.dispatchEvent(pointer('pointerup', 1100));
  expect(shell.style.getPropertyValue('--panel-width')).toBe(before);
});

test('键盘可调宽度', () => {
  const { shell, resizer } = mount();
  resizer.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
  expect(widthOf(shell)).toBe(PANEL_W0 + 20);
  resizer.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  expect(widthOf(shell)).toBe(PANEL_W0);
});
