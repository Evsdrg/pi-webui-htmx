import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { closeMobileSidebar, mountLayout } from '@/modules/layout';

let unmount: () => void;

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal('matchMedia', () => ({ matches: true }));
  document.body.innerHTML = `<div id="workbench" data-sidebar="open" data-mobile-sidebar="closed">
    <aside id="sidebar"><button data-action="sidebar" aria-label="关闭侧栏">关闭</button></aside>
    <button data-action="sidebar" aria-controls="sidebar" aria-expanded="false">打开</button>
    <div class="sidebar-resizer"></div><select id="theme-select"><option value="system">系统</option></select>
  </div><button class="sidebar-backdrop" data-action="sidebar" hidden>遮罩</button>`;
  unmount = mountLayout();
});
afterEach(() => { unmount(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

describe('侧栏 aria-expanded 与实际移动面板同步', () => {
  it('从页眉打开，从面板内关闭后 aria-expanded=false', () => {
    const control = document.querySelector<HTMLElement>('[aria-controls="sidebar"]')!;
    control.click();
    expect(control.getAttribute('aria-expanded')).toBe('true');
    document.querySelector<HTMLElement>('[aria-label="关闭侧栏"]')!.click();
    expect(document.getElementById('workbench')?.dataset.mobileSidebar).toBe('closed');
    expect(control.getAttribute('aria-expanded')).toBe('false');
  });

  it('Escape 与选择会话关闭时也要更新展开状态', () => {
    const control = document.querySelector<HTMLElement>('[aria-controls="sidebar"]')!;
    control.click();
    closeMobileSidebar();
    expect(control.getAttribute('aria-expanded')).toBe('false');
    control.click();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(control.getAttribute('aria-expanded')).toBe('false');
  });

  it('视口在移动/桌面之间变化时读对应面板状态', () => {
    const control = document.querySelector<HTMLElement>('[aria-controls="sidebar"]')!;
    expect(control.getAttribute('aria-expanded')).toBe('false');
    vi.stubGlobal('matchMedia', () => ({ matches: false }));
    window.dispatchEvent(new Event('resize'));
    expect(control.getAttribute('aria-expanded')).toBe('true');
    control.click();
    expect(control.getAttribute('aria-expanded')).toBe('false');
    vi.stubGlobal('matchMedia', () => ({ matches: true }));
    window.dispatchEvent(new Event('resize'));
    expect(control.getAttribute('aria-expanded')).toBe('false');
  });
});
