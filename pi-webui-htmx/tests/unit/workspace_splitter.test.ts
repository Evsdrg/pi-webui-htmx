// 分隔条拖动：边界要 1:1 跟手，在物理极限处精确停住并持久化。
// jsdom 没有布局，几何用 getBoundingClientRect 打桩；rAF 默认同步执行。
import { afterEach, expect, test, vi } from 'vitest';
import type { BridgeClient } from '@/modules/bridge';
import { Workspace } from '@/modules/workspace';

let workspace: Workspace | undefined;

// 桌面侧栏纵向切片：内容高 820px，文件区初始 300px，会话区初始 400px。
const PARENT_H = 820;
const FILES_H0 = 300;
const SESSIONS_H0 = 400;
const MIN_FILES_H = 64;
const MIN_SESSIONS_H = 60;

function rect(height: number): DOMRect {
  return { height, top: 0, bottom: height, left: 0, right: 0, width: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
}

function mountSplitter(): { splitter: HTMLElement; section: HTMLElement } {
  document.body.innerHTML = `
    <aside id="sidebar">
      <nav id="session-list"></nav>
      <section id="sidebar-files">
        <div class="sidebar-splitter" role="separator" aria-orientation="horizontal" tabindex="0"></div>
        <header class="sidebar-files-head"><span id="sidebar-file-path"></span><button id="files-collapse"></button></header>
        <div id="sidebar-files-body"><input id="files-path" type="hidden"><nav id="file-list"></nav></div>
      </section>
    </aside>
    <aside id="workspace-panel"></aside>`;
  const parent = document.getElementById('sidebar')!;
  parent.style.paddingTop = '0px';
  parent.style.paddingBottom = '0px';
  vi.spyOn(parent, 'getBoundingClientRect').mockReturnValue(rect(PARENT_H));
  vi.spyOn(document.getElementById('sidebar-files')!, 'getBoundingClientRect').mockReturnValue(rect(FILES_H0));
  vi.spyOn(document.getElementById('session-list')!, 'getBoundingClientRect').mockReturnValue(rect(SESSIONS_H0));
  // jsdom 没有指针捕获（实测 Element.prototype.setPointerCapture 为 undefined）。
  Object.defineProperty(Element.prototype, 'setPointerCapture', { configurable: true, value: vi.fn() });
  Object.defineProperty(Element.prototype, 'releasePointerCapture', { configurable: true, value: vi.fn() });
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { callback(0); return 1; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
  vi.stubGlobal('htmx', { ajax: vi.fn().mockResolvedValue(undefined), trigger: vi.fn() });
  const bridge = Object.assign(new EventTarget(), { request: vi.fn().mockResolvedValue({ roots: ['/repo'] }) });
  workspace = new Workspace(bridge as unknown as BridgeClient, vi.fn());
  return { splitter: document.querySelector('.sidebar-splitter')!, section: document.getElementById('sidebar-files')! };
}

const pointer = (type: string, clientY: number) => new PointerEvent(type, { clientY, pointerId: 1, bubbles: true });
const ratioOf = (section: HTMLElement) => Number(section.style.getPropertyValue('--files-ratio'));

afterEach(() => {
  workspace?.dispose();
  vi.unstubAllGlobals();
  localStorage.clear();
  document.body.replaceChildren();
  delete (Element.prototype as { setPointerCapture?: unknown }).setPointerCapture;
  delete (Element.prototype as { releasePointerCapture?: unknown }).releasePointerCapture;
});

test('拖动 1:1 跟手：上移 100px 文件区就长 100px，松手落盘', () => {
  const { splitter, section } = mountSplitter();
  expect(splitter.getAttribute('aria-valuenow')).toBe('45');
  splitter.dispatchEvent(pointer('pointerdown', 400));
  splitter.dispatchEvent(pointer('pointermove', 300));
  expect(ratioOf(section)).toBeCloseTo((FILES_H0 + 100) / PARENT_H, 10);
  expect(splitter.getAttribute('aria-valuenow')).toBe(String(Math.round(((FILES_H0 + 100) / PARENT_H) * 100)));
  expect(localStorage.getItem('pi-ui:sidebar-files-ratio')).toBeNull();
  splitter.dispatchEvent(pointer('pointerup', 300));
  expect(Number(localStorage.getItem('pi-ui:sidebar-files-ratio'))).toBeCloseTo((FILES_H0 + 100) / PARENT_H, 10);
});

test('上限停在「会话区剩 60px」处，继续拖不再变化', () => {
  const { splitter, section } = mountSplitter();
  const maxH = FILES_H0 + (SESSIONS_H0 - MIN_SESSIONS_H); // 300 + 340 = 640
  splitter.dispatchEvent(pointer('pointerdown', 400));
  splitter.dispatchEvent(pointer('pointermove', -1000));
  expect(ratioOf(section)).toBeCloseTo(maxH / PARENT_H, 10);
  splitter.dispatchEvent(pointer('pointermove', -5000));
  expect(ratioOf(section)).toBeCloseTo(maxH / PARENT_H, 10);
});

test('下限停在空中 64px 处，继续拖不再变化', () => {
  const { splitter, section } = mountSplitter();
  splitter.dispatchEvent(pointer('pointerdown', 400));
  splitter.dispatchEvent(pointer('pointermove', 10000));
  expect(ratioOf(section)).toBeCloseTo(MIN_FILES_H / PARENT_H, 10);
  splitter.dispatchEvent(pointer('pointermove', 20000));
  expect(ratioOf(section)).toBeCloseTo(MIN_FILES_H / PARENT_H, 10);
});

test('同一帧内多次移动只应用最后一次（rAF 合帧）', () => {
  const { splitter, section } = mountSplitter();
  const frames: FrameRequestCallback[] = [];
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { frames.push(callback); return frames.length; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
  splitter.dispatchEvent(pointer('pointerdown', 400));
  splitter.dispatchEvent(pointer('pointermove', 300));
  splitter.dispatchEvent(pointer('pointermove', 280));
  expect(frames.length).toBe(1);
  expect(ratioOf(section)).toBeCloseTo(0.45, 10);
  frames.shift()?.(0);
  expect(ratioOf(section)).toBeCloseTo((FILES_H0 + 120) / PARENT_H, 10);
});

test('键盘 ↑/↓ 也走几何钳制并落盘', () => {
  const { splitter, section } = mountSplitter();
  splitter.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
  expect(ratioOf(section)).toBeCloseTo((FILES_H0 + 0.05 * PARENT_H) / PARENT_H, 10);
  expect(splitter.getAttribute('aria-valuenow')).toBe('42');
  expect(Number(localStorage.getItem('pi-ui:sidebar-files-ratio'))).toBeCloseTo((FILES_H0 + 0.05 * PARENT_H) / PARENT_H, 10);
});

test('折叠态下拖动分隔条无效果', () => {
  localStorage.setItem('pi-ui:sidebar-files-collapsed', '1');
  const { splitter, section } = mountSplitter();
  expect(section.classList.contains('is-collapsed')).toBe(true);
  splitter.dispatchEvent(pointer('pointerdown', 400));
  splitter.dispatchEvent(pointer('pointermove', 100));
  expect(ratioOf(section)).toBeCloseTo(0.45, 10);
  expect(splitter.getAttribute('aria-valuenow')).toBe('45');
});

test('旧的裸键偏好会被读取并迁移到 pi-ui: 命名空间', () => {
  localStorage.setItem('sidebar-files-ratio', '0.6');
  localStorage.setItem('sidebar-files-collapsed', '1');
  const { section } = mountSplitter();
  expect(ratioOf(section)).toBeCloseTo(0.6, 10);
  expect(section.classList.contains('is-collapsed')).toBe(true);
  expect(localStorage.getItem('pi-ui:sidebar-files-ratio')).toBe('0.6');
  expect(localStorage.getItem('pi-ui:sidebar-files-collapsed')).toBe('1');
});
