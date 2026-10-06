import { afterEach, expect, it, vi } from 'vitest';
import { Workspace } from '@/modules/workspace';
import type { BridgeClient } from '@/modules/bridge';

let workspace: Workspace;

// stubBlobUrls 记录 blob URL 的创建与释放：jsdom 不实现这两个 API，
// 而「换预览时释放旧图」正是要用它断言的行为。
function stubBlobUrls(): { created: string[]; revoked: string[] } {
  const created: string[] = [];
  const revoked: string[] = [];
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    writable: true,
    value: () => { const url = `blob:test-${created.length}`; created.push(url); return url; },
  });
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    writable: true,
    value: (url: string) => { revoked.push(url); },
  });
  return { created, revoked };
}

function mount(): { bridge: { request: ReturnType<typeof vi.fn> }; fetchMock: ReturnType<typeof vi.fn> } {
  document.body.innerHTML = '<aside id="sidebar"></aside><aside id="workspace-panel"><button data-action="file-close">关闭</button></aside><span id="file-name"></span><div id="panel-preview" hidden><div id="file-content"></div></div>';
  document.getElementById('sidebar')!.innerHTML = '<button data-file-path="/repo/a.png" data-directory="false">图</button>';
  const fetchMock = vi.fn(async () => new Response(new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: 'image/png' }), { status: 200, headers: { 'Content-Type': 'image/png' } }));
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('htmx', { trigger: vi.fn(), ajax: vi.fn() });
  const bridge = { request: vi.fn().mockResolvedValue({}) };
  workspace = new Workspace(Object.assign(new EventTarget(), bridge) as unknown as BridgeClient, vi.fn());
  return { bridge, fetchMock };
}

afterEach(() => { workspace?.dispose(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

it('图片预览走 HTTP，不再占用 WS 帧预算', async () => {
  // WS 单帧上限 512 KiB，而图片允许到 4 MiB：走 WS 时大图会被连接层
  // 整帧丢掉，命令只剩超时（B33）。所以图片必须走 HTTP。
  const { bridge, fetchMock } = mount();
  stubBlobUrls();
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(document.querySelector('#panel-preview img')).toBeTruthy());
  expect(bridge.request).not.toHaveBeenCalled();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain('ui/file-image');
});

it('换预览时释放上一张图的 blob URL', async () => {
  const { created, revoked } = stubBlobUrls();
  mount();
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(document.querySelector('#panel-preview img')).toBeTruthy());
  // 同一张图再来一次：新 URL 建出来之后，旧的必须被释放。
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(created.length).toBeGreaterThan(1));
  expect(revoked).toContain(created[0]);
});

it('关闭文件预览时释放图片 URL', async () => {
  const { created, revoked } = stubBlobUrls();
  mount();
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(created.length).toBe(1));
  // 关闭预览以前只清 DOM，对象 URL 留到页面卸载（R01）。
  document.querySelector<HTMLButtonElement>('[data-action="file-close"]')!.click();
  expect(revoked).toContain(created[0]);
});

it('销毁工作区时释放图片 URL', async () => {
  const { created, revoked } = stubBlobUrls();
  mount();
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(created.length).toBe(1));
  workspace.dispose();
  expect(revoked).toContain(created[0]);
});
