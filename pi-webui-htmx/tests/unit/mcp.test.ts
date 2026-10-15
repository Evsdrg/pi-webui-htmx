import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { McpEditor } from '@/modules/mcp';
import type { BridgeClient } from '@/modules/bridge';
import { readFileSync } from 'node:fs';

let editor: McpEditor;
let writes: any[];
let doc: any;

const input = (id: string) => document.getElementById(id) as HTMLInputElement;
const area = (id: string) => document.getElementById(id) as HTMLTextAreaElement;
const select = (id: string) => document.getElementById(id) as HTMLSelectElement;

beforeEach(() => {
  // 切出真实的 mcp-dialog 片段，避免与模板漂移。
  const shell = readFileSync('src/templates/shell.html', 'utf8');
  document.body.innerHTML = shell.slice(shell.indexOf('<dialog id="mcp-dialog"'), shell.indexOf('<dialog id="branch-dialog"'));
  // jsdom 不实现原生 dialog 的模态方法。
  HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  HTMLDialogElement.prototype.close = function () { this.open = false; };
  doc = {
    mcpServers: {
      fs: { command: 'npx', args: ['-y', 'server-filesystem'], env: { TOKEN: '***' }, exposure: 'direct', description: '文件' },
      docs: { url: 'https://example.com/mcp', headers: { Authorization: '***' }, enabled: false },
    },
  };
  writes = [];
  const htmx = { trigger: vi.fn() };
  (window as any).htmx = htmx;
  editor = new McpEditor({
    request: async (method: string, _session: string, args: any) => {
      if (method === 'config.mcp') return { document: structuredClone(doc), servers: [] };
      writes.push(args.document);
      return { written: true };
    },
  } as unknown as BridgeClient, () => {});
});
afterEach(() => { vi.restoreAllMocks(); document.body.replaceChildren(); delete (window as any).htmx; });

it('编辑服务器：表单回填秘密为 *** 且名称只读', async () => {
  await editor.openEdit('fs');
  expect(input('mc-name').value).toBe('fs');
  expect(input('mc-name').disabled).toBe(true);
  expect(select('mc-transport').value).toBe('stdio');
  expect(input('mc-command').value).toBe('npx');
  expect(area('mc-args').value).toBe('-y\nserver-filesystem');
  expect(area('mc-env').value).toBe('TOKEN=***');
  expect(select('mc-exposure').value).toBe('direct');
});

it('编辑 http 服务器时切到 http 传输', async () => {
  await editor.openEdit('docs');
  expect(select('mc-transport').value).toBe('http');
  expect(input('mc-url').value).toBe('https://example.com/mcp');
  expect(area('mc-headers').value).toBe('Authorization: ***');
  expect(input('mc-enabled').checked).toBe(false);
});

it('保存编辑：保留 *** 并写回文档', async () => {
  await editor.openEdit('fs');
  input('mc-description').value = '改了描述';
  await editor.save();
  expect(writes).toHaveLength(1);
  expect(writes[0].mcpServers.fs.description).toBe('改了描述');
  expect(writes[0].mcpServers.fs.env.TOKEN).toBe('***');
});

it('新增服务器：名称非法时不写盘', async () => {
  await editor.openAdd();
  input('mc-name').value = 'bad name';
  input('mc-command').value = 'npx';
  await editor.save();
  expect(writes).toHaveLength(0);
  expect(document.getElementById('mcp-status')!.textContent).toContain('名称');
});

it('新增 http 服务器：URL 为空不写盘', async () => {
  await editor.openAdd();
  input('mc-name').value = 'remote';
  select('mc-transport').value = 'http';
  await editor.save();
  expect(writes).toHaveLength(0);
});

it('新增服务器：合法输入写回并触发列表刷新', async () => {
  await editor.openAdd();
  input('mc-name').value = 'new-srv';
  input('mc-command').value = 'npx';
  area('mc-args').value = '-y\npkg';
  await editor.save();
  expect(writes[0].mcpServers['new-srv']).toMatchObject({ command: 'npx', args: ['-y', 'pkg'] });
  expect((window as any).htmx.trigger).toHaveBeenCalledWith(document.body, 'mcp-refresh');
});

it('启停切换翻转 enabled 且不改其它字段', async () => {
  await editor.toggle('docs');
  expect(writes[0].mcpServers.docs.enabled).toBe(true);
  expect(writes[0].mcpServers.docs.url).toBe('https://example.com/mcp');
  expect(writes[0].mcpServers.docs.headers.Authorization).toBe('***');
});

it('删除服务器移除该条目', async () => {
  await editor.remove('fs');
  expect(writes[0].mcpServers.fs).toBeUndefined();
  expect(writes[0].mcpServers.docs).toBeDefined();
});
