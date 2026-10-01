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


// 会话视图的选中态只由偏好（与服务端片段）决定。
//
// 反例是实测出来的：浏览器在 reload 时会恢复表单控件的值，于是控件显示
// 「按工作区」而列表按时间线渲染——控件与内容对不上，用户点一下才会发现
// 自己看到的不是选中的那个视图。所以视图控件用按钮（不在恢复范围内），
// 选中态与随请求发送的 view 值都从偏好推导。
describe('会话列表视图切换', () => {
  const withSwitch = () => {
    document.body.insertAdjacentHTML('beforeend', `<div class="view-switch" id="view-switch" role="group">
      <button type="button" class="view-btn" data-view="timeline" aria-pressed="true">时间线</button>
      <button type="button" class="view-btn" data-view="workspace" aria-pressed="false">按工作区</button>
      <input type="hidden" id="session-view-value" name="view" value="timeline">
    </div>`);
    return document.getElementById('view-switch')!;
  };
  const pressed = () => [...document.querySelectorAll<HTMLElement>('.view-btn')]
    .filter((b) => b.getAttribute('aria-pressed') === 'true').map((b) => b.dataset.view);
  const carrier = () => (document.getElementById('session-view-value') as HTMLInputElement).value;

  beforeEach(() => {
    vi.stubGlobal('htmx', { trigger: vi.fn() });
  });

  it('没有偏好时选中时间线，且随请求发送的是 timeline', () => {
    withSwitch();
    // 模拟 DOM 上残留着别的选择（例如浏览器恢复或服务端渲染成别的视图）。
    document.querySelector<HTMLElement>('.view-btn[data-view="workspace"]')!.setAttribute('aria-pressed', 'true');
    unmount(); unmount = mountLayout();
    expect(pressed()).toEqual(['timeline']);
    expect(carrier()).toBe('timeline');
  });

  it('有偏好时按偏好选中，并立刻按它加载一次', () => {
    localStorage.setItem('pi-ui:session-view', 'workspace');
    withSwitch();
    unmount(); unmount = mountLayout();
    expect(pressed()).toEqual(['workspace']);
    expect(carrier()).toBe('workspace');
    expect(window.htmx.trigger).toHaveBeenCalled();
  });

  it('点击另一个视图会更新选中态、随请求的值与偏好', () => {
    withSwitch();
    unmount(); unmount = mountLayout();
    document.querySelector<HTMLElement>('.view-btn[data-view="workspace"]')!.click();
    expect(pressed()).toEqual(['workspace']);
    expect(carrier()).toBe('workspace');
    expect(localStorage.getItem('pi-ui:session-view')).toBe('workspace');
  });

  it('片段换入后按服务端给的值对齐偏好', () => {
    localStorage.setItem('pi-ui:session-view', 'workspace');
    withSwitch();
    unmount(); unmount = mountLayout();
    expect(localStorage.getItem('pi-ui:session-view')).toBe('workspace');
    // 服务端整块换入时间线版本的控件（从分组视图点「查看全部」）。
    (document.getElementById('session-view-value') as HTMLInputElement).value = 'timeline';
    document.body.dispatchEvent(new CustomEvent('htmx:afterSwap'));
    expect(localStorage.getItem('pi-ui:session-view')).toBe('timeline');
  });
});
