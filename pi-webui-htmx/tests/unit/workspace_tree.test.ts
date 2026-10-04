// 文件树：点击目录就地展开，而不是把根切到该目录。
// 根目录的切换仍归「上一级 / 刷新 / 会话切换」，由 workspace_git 覆盖。
import { afterEach, expect, test, vi } from 'vitest';
import type { BridgeClient } from '@/modules/bridge';
import { Workspace } from '@/modules/workspace';

let workspace: Workspace | undefined;
afterEach(() => { workspace?.dispose(); vi.unstubAllGlobals(); localStorage.clear(); document.body.replaceChildren(); });

function mountTree(): { ajax: ReturnType<typeof vi.fn>; triggers: string[] } {
  document.body.innerHTML = `
    <aside id="sidebar">
      <section id="sidebar-files">
        <div class="sidebar-splitter" role="separator" aria-orientation="horizontal"></div>
        <header class="sidebar-files-head"><span id="sidebar-file-path"></span><button id="files-collapse"></button></header>
        <div id="sidebar-files-body">
          <input id="files-path" type="hidden" value="">
          <nav id="file-list">
            <div class="file-node">
              <button class="file-item" data-file-path="/repo/src" data-directory="true" aria-expanded="false"><span class="file-caret" aria-hidden="true">▸</span><span class="file-name">src</span></button>
              <div class="file-children" hidden></div>
            </div>
          </nav>
        </div>
      </section>
    </aside>
    <aside id="workspace-panel"></aside>
    <div id="git-status"></div><div id="git-diff"></div>
    <span id="file-name"></span><div id="panel-preview" hidden><div id="file-content"></div></div>`;
  const triggers: string[] = [];
  const ajax = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('htmx', { ajax, trigger: (_el: Element, name: string) => triggers.push(name) });
  const bridge = Object.assign(new EventTarget(), { request: vi.fn().mockResolvedValue({ roots: ['/repo'] }) });
  workspace = new Workspace(bridge as unknown as BridgeClient, vi.fn());
  return { ajax, triggers };
}

const dirButton = () => document.querySelector<HTMLButtonElement>('[data-file-path="/repo/src"]')!;
const children = () => document.querySelector<HTMLElement>('.file-children')!;

test('点击目录就地展开：不改根目录、不发根刷新，只拉子层片段', async () => {
  const { ajax, triggers } = mountTree();
  dirButton().click();
  expect(dirButton().getAttribute('aria-expanded')).toBe('true');
  expect(children().hidden).toBe(false);
  // 反例（旧行为）：点击目录会把 #files-path 改成该目录并触发 files-refresh。
  expect((document.getElementById('files-path') as HTMLInputElement).value).toBe('');
  expect(triggers).not.toContain('files-refresh');
  await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(1));
  expect(ajax.mock.calls[0]?.[0]).toBe('get');
  expect(String(ajax.mock.calls[0]?.[1])).toBe('ui/files?path=%2Frepo%2Fsrc');
  expect((ajax.mock.calls[0]?.[2] as { target: HTMLElement }).target).toBe(children());
  await vi.waitFor(() => expect(children().dataset.state).toBe('loaded'));
});

test('再点收起、三度展开：已装载的子层不再重新请求', async () => {
  const { ajax } = mountTree();
  dirButton().click();
  await vi.waitFor(() => expect(children().dataset.state).toBe('loaded'));
  dirButton().click();
  expect(dirButton().getAttribute('aria-expanded')).toBe('false');
  expect(children().hidden).toBe(true);
  dirButton().click();
  expect(dirButton().getAttribute('aria-expanded')).toBe('true');
  expect(children().hidden).toBe(false);
  expect(ajax).toHaveBeenCalledTimes(1);
});

test('首帧请求未返回时连点，不会重复发请求', async () => {
  const { ajax } = mountTree();
  let finish!: (value: unknown) => void;
  ajax.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  dirButton().click();
  dirButton().click();
  dirButton().click();
  expect(ajax).toHaveBeenCalledTimes(1);
  finish(undefined);
  await vi.waitFor(() => expect(children().dataset.state).toBe('loaded'));
});

test('嵌套目录逐级展开，各自请求自己的路径', async () => {
  const { ajax } = mountTree();
  dirButton().click();
  await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(1));
  children().innerHTML = `<div class="file-node">
    <button class="file-item" data-file-path="/repo/src/deep" data-directory="true" aria-expanded="false"><span class="file-caret" aria-hidden="true">▸</span><span class="file-name">deep</span></button>
    <div class="file-children" hidden></div></div>`;
  document.querySelector<HTMLButtonElement>('[data-file-path="/repo/src/deep"]')!.click();
  await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(2));
  expect(String(ajax.mock.calls[1]?.[1])).toBe('ui/files?path=%2Frepo%2Fsrc%2Fdeep');
});

test('读取失败时留在原地提示，收起再展开会重试', async () => {
  const { ajax } = mountTree();
  ajax.mockRejectedValueOnce(new Error('网络错误'));
  dirButton().click();
  await vi.waitFor(() => expect(children().textContent).toContain('失败'));
  expect(children().dataset.state).toBeUndefined();
  expect(dirButton().getAttribute('aria-expanded')).toBe('true');
  dirButton().click();
  dirButton().click();
  await vi.waitFor(() => expect(ajax).toHaveBeenCalledTimes(2));
  await vi.waitFor(() => expect(children().dataset.state).toBe('loaded'));
});
