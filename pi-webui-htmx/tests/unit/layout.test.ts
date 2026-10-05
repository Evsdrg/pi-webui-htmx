import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { closeMobileSidebar, mountLayout } from '@/modules/layout';

let unmount: () => void;

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal('matchMedia', () => ({ matches: true }));
  document.body.innerHTML = `<div id="workbench" data-sidebar="open" data-mobile-sidebar="closed">
    <aside id="sidebar"><button data-action="sidebar" aria-label="关闭侧栏">关闭</button></aside>
    <button data-action="sidebar" aria-controls="sidebar" aria-expanded="false">打开</button>
    <div class="sidebar-resizer"></div><div class="view-switch" role="group"><button class="view-btn" data-theme-mode="system">跟随系统</button><button class="view-btn" data-theme-mode="light">白天</button><button class="view-btn" data-theme-mode="dark">夜间</button></div><div class="view-switch" role="group"><button class="view-btn" data-theme-pick="light">明亮</button><button class="view-btn" data-theme-pick="mist">雾蓝</button></div><div class="view-switch" role="group"><button class="view-btn" data-theme-pick="dark">深夜蓝</button><button class="view-btn" data-theme-pick="obsidian">纯黑</button></div><button class="icon-btn" data-action="theme-toggle"><svg><use id="theme-toggle-icon" href="#icon-sun"/></svg></button>
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

describe('对话过程默认折叠偏好', () => {
  const mountProcessFixture = () => {
    document.body.insertAdjacentHTML('beforeend', `<label><input id="process-collapsed" type="checkbox"></label>
      <div id="turns"><details class="turn-process"><summary>过程</summary></details></div>`);
  };

  it('没有偏好时默认勾选且历史过程保持折叠', () => {
    mountProcessFixture();
    unmount(); unmount = mountLayout();
    const setting = document.querySelector<HTMLInputElement>('#process-collapsed')!;
    expect(setting.checked).toBe(true);
    expect(document.querySelector<HTMLDetailsElement>('.turn-process')!.open).toBe(false);
  });

  it('取消偏好后展开当前及新换入的历史过程，并保存选择', () => {
    localStorage.setItem('pi-ui:process-collapsed', '1');
    mountProcessFixture();
    unmount(); unmount = mountLayout();
    const setting = document.querySelector<HTMLInputElement>('#process-collapsed')!;
    setting.checked = false;
    setting.dispatchEvent(new Event('change', { bubbles: true }));
    expect(localStorage.getItem('pi-ui:process-collapsed')).toBe('0');
    expect(document.querySelector<HTMLDetailsElement>('.turn-process')!.open).toBe(true);
    const target = document.getElementById('turns')!;
    target.insertAdjacentHTML('beforeend', '<details class="turn-process"><summary>新过程</summary></details>');
    document.body.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target } }));
    expect([...target.querySelectorAll<HTMLDetailsElement>('.turn-process')].every((node) => node.open)).toBe(true);
  });

  it('无关片段换入不覆盖用户手动展开状态', () => {
    mountProcessFixture();
    unmount(); unmount = mountLayout();
    const process = document.querySelector<HTMLDetailsElement>('.turn-process')!;
    process.open = true;
    document.body.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target: document.body } }));
    expect(process.open).toBe(true);
  });
});

describe('对话内容宽度：顶满可用宽度开关', () => {
  const mountWidthFixture = () => {
    document.body.insertAdjacentHTML('beforeend', `<div id="main"></div>
      <input id="chat-width" type="range" min="640" max="1400" step="20" value="820">
      <span id="chat-width-value"></span>
      <label class="switch"><input id="chat-width-full" type="checkbox"><span>顶满可用宽度</span></label>`);
  };
  const widthVar = () => (document.getElementById('main') as HTMLElement).style.getPropertyValue('--chat-width');
  const slider = () => document.getElementById('chat-width') as HTMLInputElement;
  const toggle = () => document.getElementById('chat-width-full') as HTMLInputElement;

  it('没有偏好时开关未勾选，宽度取滑块值', () => {
    mountWidthFixture();
    unmount(); unmount = mountLayout();
    expect(toggle().checked).toBe(false);
    expect(slider().disabled).toBe(false);
    expect(widthVar()).toBe('820px');
  });

  it('勾选后宽度顶满、滑块禁用、偏好保存', () => {
    mountWidthFixture();
    unmount(); unmount = mountLayout();
    toggle().checked = true;
    toggle().dispatchEvent(new Event('change', { bubbles: true }));
    expect(widthVar()).toBe('100%');
    expect(slider().disabled).toBe(true);
    expect(document.getElementById('chat-width-value')!.textContent).toBe('顶满');
    expect(localStorage.getItem('pi-ui:chat-width-full')).toBe('1');
  });

  it('有顶满偏好时按偏好加载', () => {
    localStorage.setItem('pi-ui:chat-width-full', '1');
    mountWidthFixture();
    unmount(); unmount = mountLayout();
    expect(toggle().checked).toBe(true);
    expect(widthVar()).toBe('100%');
    expect(slider().disabled).toBe(true);
  });

  it('取消勾选后恢复滑块宽度并允许再调', () => {
    localStorage.setItem('pi-ui:chat-width-full', '1');
    localStorage.setItem('pi-ui:chat-width', '1100');
    mountWidthFixture();
    unmount(); unmount = mountLayout();
    expect(widthVar()).toBe('100%');
    toggle().checked = false;
    toggle().dispatchEvent(new Event('change', { bubbles: true }));
    expect(widthVar()).toBe('1100px');
    expect(slider().disabled).toBe(false);
    expect(localStorage.getItem('pi-ui:chat-width-full')).toBe('0');
  });

  it('顶满状态下滑块事件不会把宽度拉回像素值', () => {
    mountWidthFixture();
    unmount(); unmount = mountLayout();
    toggle().checked = true;
    toggle().dispatchEvent(new Event('change', { bubbles: true }));
    slider().value = '1400';
    slider().dispatchEvent(new Event('input', { bubbles: true }));
    expect(widthVar()).toBe('100%');
  });
});
