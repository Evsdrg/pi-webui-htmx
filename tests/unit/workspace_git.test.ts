// 工作区现在的更新动作都是声明式的（htmx 属性 + 隐藏输入），
// 这里只测留在浏览器里的那一层：把目录写进隐藏输入并触发刷新。
// 「Git 变更列表」的排版与截断文案已移到桥侧渲染，对应断言在 Go 的
// internal/presentation 单测里（截断时必须说明是列表被截断，不能宣称干净）。
import { afterEach, expect, test, vi } from 'vitest';
import type { BridgeClient } from '@/modules/bridge';
import { Workspace } from '@/modules/workspace';

let workspace: Workspace | undefined;
afterEach(() => { workspace?.dispose(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

function mountWorkspace(): { bridge: BridgeClient; triggers: string[]; onError: ReturnType<typeof vi.fn> } {
  // 文件树已从右面板移到侧栏常驻（对齐 Pi Web：会话列表在上、文件浏览器在下）。
  // 夹具要照这个结构搭，否则挂不上点击监听，测的就是不存在的那条路径。
  document.body.innerHTML = `
    <aside id="sidebar">
      <section id="sidebar-files">
        <div class="sidebar-splitter" role="separator" aria-orientation="horizontal"></div>
        <header class="sidebar-files-head"><span id="sidebar-file-path"></span></header>
        <div id="sidebar-files-body"></div>
      </section>
    </aside>
    <aside id="workspace-panel"><button data-panel="git">Git</button></aside>
    <input id="files-path" type="hidden"><input id="git-path" type="hidden"><input id="diff-path" type="hidden">
    <div id="file-list"></div><div id="git-status"></div><div id="git-diff"></div>
    <div id="file-preview" hidden><code id="file-content"></code></div>`;
  const triggers: string[] = [];
  vi.stubGlobal('htmx', {
    ajax: vi.fn().mockResolvedValue(undefined),
    trigger: (_el: Element, name: string) => triggers.push(name),
  });
  const bridge = Object.assign(new EventTarget(), {
    request: vi.fn().mockResolvedValue({ branch: 'main', clean: true, truncated: false, files: [] }),
  });
  const onError = vi.fn();
  workspace = new Workspace(bridge as unknown as BridgeClient, onError);
  return { bridge: bridge as unknown as BridgeClient, triggers, onError };
}

test('进入目录时写隐藏输入并触发片段刷新，而不是自己拼 URL 写 DOM', async () => {
  const { triggers } = mountWorkspace();
  document.getElementById('sidebar-files-body')!.insertAdjacentHTML('beforeend', '<button data-file-path="/repo/src" data-directory="true">src</button>');
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  expect(document.getElementById('files-path')!.getAttribute('value')).toBe('/repo/src');
  expect(triggers).toContain('files-refresh');
});

test('切换「变更」标签把路径交给两个声明式片段请求', async () => {
  const { triggers } = mountWorkspace();
  workspace!.setCwd('/repo');
  document.querySelector<HTMLButtonElement>('[data-panel="git"]')!.click();
  await vi.waitFor(() => expect(triggers).toContain('diff-refresh'));
  // 状态列表与差异都由桥渲染，两块共用当前目录。
  expect(triggers).toContain('git-status-refresh');
  expect((document.getElementById('diff-path') as HTMLInputElement).value).toBe('/repo');
  expect((document.getElementById('git-path') as HTMLInputElement).value).toBe('/repo');
});

test('迟到的文件列表响应被拒绝交换（按当前目录判定）', async () => {
  mountWorkspace();
  const panel = document.getElementById('workspace-panel')!;
  document.getElementById('sidebar-files-body')!.insertAdjacentHTML('beforeend', '<button data-file-path="/repo/a" data-directory="true">a</button><button data-file-path="/repo/b" data-directory="true">b</button>');
  const [a, b] = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-file-path]'));
  a!.click();
  b!.click();
  // 请求 a 的响应在切换到 b 之后才回来：必须不交换。
  const swap = (path: string) => {
    const detail = { target: document.getElementById('file-list'), shouldSwap: true, xhr: { responseURL: `http://localhost/ui/files?path=${encodeURIComponent(path)}` } };
    document.dispatchEvent(new CustomEvent('htmx:beforeSwap', { detail, cancelable: true }));
    return detail.shouldSwap;
  };
  expect(swap('/repo/a')).toBe(false);
  expect(swap('/repo/b')).toBe(true);
});

// D 批次把文件树移进侧栏后，workspace 在 start() 里就被装载，但刷新页面
// 时没人调 setCwd——它的「当前目录」是空的，而守卫用这个空值判定，
// 于是外部发起（workbench 启动恢复、HTML 的 hx-trigger）的响应全被丢掉，
// 文件树停在空提示上。没有当前目录时不该做「迟到」判定。
test('还没有当前目录时不拦外部发起的文件树响应', () => {
  mountWorkspace();
  const detail = {
    target: document.getElementById('file-list'),
    shouldSwap: true,
    xhr: { responseURL: 'http://localhost/ui/files?path=%2Frepo' },
  };
  document.dispatchEvent(new CustomEvent('htmx:beforeSwap', { detail, cancelable: true }));
  expect(detail.shouldSwap).toBe(true);
});
