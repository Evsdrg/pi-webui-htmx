import { afterEach, expect, test, vi } from 'vitest';
import type { BridgeClient } from '@/modules/bridge';
import { Workspace } from '@/modules/workspace';

let workspace: Workspace | undefined;
afterEach(() => { workspace?.dispose(); vi.unstubAllGlobals(); document.body.replaceChildren(); });

test('Git 截断状态明确显示，不宣称工作区干净', async () => {
  document.body.innerHTML = `<aside id="workspace-panel"><button data-panel="git">Git</button></aside>
    <div id="file-list"></div><div id="file-preview"></div><div id="git-status"></div><div id="git-diff"></div>`;
  const bridge = Object.assign(new EventTarget(), {
    request: vi.fn().mockResolvedValue({ branch: 'main', clean: true, truncated: true, files: [] }),
  });
  vi.stubGlobal('htmx', { ajax: vi.fn().mockResolvedValue(undefined) });
  const onError = vi.fn();
  workspace = new Workspace(bridge as unknown as BridgeClient, onError);
  workspace.setCwd('/repo');
  document.querySelector<HTMLButtonElement>('[data-panel="git"]')!.click();
  await vi.waitFor(() => expect(document.getElementById('git-status')!.textContent).toContain('列表已截断'));
  expect(document.getElementById('git-status')!.textContent).not.toContain('工作区干净');
  expect(onError).not.toHaveBeenCalled();
});
