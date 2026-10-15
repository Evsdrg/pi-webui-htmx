// MCP 服务器管理。凭据只留在桥所在的机器，页面永远拿不到真实值：
// 读回来时 env/headers/clientSecret 已被桥打码成 "***"，保存时由桥按磁盘真值还原。
//
// 作用域只覆盖全局 mcp.json。项目级 .pi/mcp.json 受 project trust 限制，
// 桥运行时本就拒绝项目信任，因此不在这里管理。
//
// 列表本身由桥渲染（/ui/mcp 片段，含连接状态）；本模块负责对话框（新增/编辑）
// 与行内动作（启停/删除）——这些要改写文档，只能走 config.mcp.write。
import type { BridgeClient } from './bridge';
import { record, text } from './stream';
import { el, elOrNull, openDialog, closeDialog } from './dom';

type McpServerEntry = {
  command?: string;
  args?: string[];
  url?: string;
  type?: string;
  cwd?: string;
  env?: Record<string, string>;
  headers?: Record<string, string>;
  exposure?: string;
  description?: string;
  enabled?: boolean;
  [key: string]: unknown;
};
type McpDoc = { mcpServers?: Record<string, McpServerEntry> } & Record<string, unknown>;

/** 「每行 name=value」转对象；空行跳过。 */
function parseAssignments(raw: string, separator: string): Record<string, string> {
  const out: Record<string, string> = Object.create(null);
  for (const line of raw.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const at = trimmed.indexOf(separator);
    if (at <= 0) throw new Error(`格式应为「名称${separator}值」`);
    const name = trimmed.slice(0, at).trim();
    const value = trimmed.slice(at + 1).trim();
    if (!name) throw new Error('名称不能为空');
    out[name] = value;
  }
  return out;
}

function formatAssignments(map: unknown, separator: string): string {
  return Object.entries(record(map)).map(([name, value]) => `${name}${separator}${typeof value === 'string' ? value : String(value)}`).join('\n');
}

export class McpEditor {
  private doc: McpDoc = {};
  private editing = '';
  private saving = false;
  private abort = new AbortController();
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {
    const dialog = document.getElementById('mcp-dialog');
    dialog?.addEventListener('close', () => { this.editing = ''; }, { signal: this.abort.signal });
    // 传输方式切换：stdio 显示命令/参数/环境变量，http 显示 URL/头部。
    elOrNull<HTMLSelectElement>('mc-transport')?.addEventListener('change', () => this.syncTransport(), { signal: this.abort.signal });
  }

  dispose(): void { this.abort.abort(); }

  /** 拉取一份最新配置，作为编辑与行内动作的基准。 */
  private async load(): Promise<McpDoc> {
    const reply = record(await this.bridge.request('config.mcp', '', undefined, 15_000));
    this.doc = (record(reply.document) as McpDoc) ?? {};
    return this.doc;
  }

  private servers(): Record<string, McpServerEntry> {
    const servers = this.doc.mcpServers;
    return servers && typeof servers === 'object' ? servers : {};
  }

  /** 打开「新增服务器」对话框。 */
  async openAdd(): Promise<void> {
    await this.load();
    this.editing = '';
    el('mcp-dialog-title').textContent = '新增 MCP 服务器';
    el('mcp-status').textContent = '';
    el<HTMLInputElement>('mc-name').value = '';
    el<HTMLInputElement>('mc-name').disabled = false;
    el<HTMLSelectElement>('mc-transport').value = 'stdio';
    el<HTMLInputElement>('mc-command').value = '';
    el<HTMLTextAreaElement>('mc-args').value = '';
    el<HTMLInputElement>('mc-cwd').value = '';
    el<HTMLInputElement>('mc-url').value = '';
    el<HTMLTextAreaElement>('mc-env').value = '';
    el<HTMLTextAreaElement>('mc-headers').value = '';
    el<HTMLSelectElement>('mc-exposure').value = 'codemode';
    el<HTMLInputElement>('mc-description').value = '';
    el<HTMLInputElement>('mc-enabled').checked = true;
    this.syncTransport();
    openDialog('mcp-dialog');
  }

  /** 打开「编辑服务器」对话框，表单填成当前值（秘密为 ***）。 */
  async openEdit(name: string): Promise<void> {
    await this.load();
    const entry = this.servers()[name];
    if (!entry) { this.onError(new Error('服务器不存在，可能已被外部改动')); return; }
    this.editing = name;
    el('mcp-dialog-title').textContent = `编辑 ${name}`;
    el('mcp-status').textContent = '';
    el<HTMLInputElement>('mc-name').value = name;
    el<HTMLInputElement>('mc-name').disabled = true;
    const isHttp = typeof entry.url === 'string';
    el<HTMLSelectElement>('mc-transport').value = isHttp ? 'http' : 'stdio';
    el<HTMLInputElement>('mc-command').value = text(entry.command);
    el<HTMLTextAreaElement>('mc-args').value = Array.isArray(entry.args) ? entry.args.map((a) => String(a)).join('\n') : '';
    el<HTMLInputElement>('mc-cwd').value = text(entry.cwd);
    el<HTMLInputElement>('mc-url').value = text(entry.url);
    el<HTMLTextAreaElement>('mc-env').value = formatAssignments(entry.env, '=');
    el<HTMLTextAreaElement>('mc-headers').value = formatAssignments(entry.headers, ': ');
    el<HTMLSelectElement>('mc-exposure').value = text(entry.exposure) || 'codemode';
    el<HTMLInputElement>('mc-description').value = text(entry.description);
    el<HTMLInputElement>('mc-enabled').checked = entry.enabled !== false;
    this.syncTransport();
    openDialog('mcp-dialog');
  }

  private syncTransport(): void {
    const mode = el<HTMLSelectElement>('mc-transport').value;
    for (const field of document.querySelectorAll<HTMLElement>('[data-mcp-transport]')) {
      field.hidden = field.dataset.mcpTransport !== mode;
    }
  }

  /** 收集表单为一个服务器条目；失败时返回 null 并显示原因。 */
  private collect(): [string, McpServerEntry] | null {
    try {
      const name = el<HTMLInputElement>('mc-name').value.trim();
      if (!/^[A-Za-z0-9_-]+$/.test(name)) throw new Error('名称只能包含字母、数字、下划线与连字符');
      const mode = el<HTMLSelectElement>('mc-transport').value;
      const entry: McpServerEntry = {};
      if (mode === 'http') {
        const url = el<HTMLInputElement>('mc-url').value.trim();
        if (!url) throw new Error('URL 不能为空');
        entry.url = url;
        const headers = parseAssignments(el<HTMLTextAreaElement>('mc-headers').value, ':');
        if (Object.keys(headers).length) entry.headers = headers;
      } else {
        const command = el<HTMLInputElement>('mc-command').value.trim();
        if (!command) throw new Error('命令不能为空');
        entry.command = command;
        const args = el<HTMLTextAreaElement>('mc-args').value.split('\n').map((a) => a.trim()).filter(Boolean);
        if (args.length) entry.args = args;
        const cwd = el<HTMLInputElement>('mc-cwd').value.trim();
        if (cwd) entry.cwd = cwd;
        const env = parseAssignments(el<HTMLTextAreaElement>('mc-env').value, '=');
        if (Object.keys(env).length) entry.env = env;
      }
      const exposure = el<HTMLSelectElement>('mc-exposure').value;
      if (exposure && exposure !== 'codemode') entry.exposure = exposure;
      const description = el<HTMLInputElement>('mc-description').value.trim();
      if (description) entry.description = description;
      if (!el<HTMLInputElement>('mc-enabled').checked) entry.enabled = false;
      return [name, entry];
    } catch (error) {
      el('mcp-status').textContent = error instanceof Error ? error.message : '表单内容无效';
      return null;
    }
  }

  /** 保存当前对话框（新增或编辑）。 */
  async save(): Promise<void> {
    if (this.saving) return;
    const collected = this.collect();
    if (!collected) return;
    const [name, entry] = collected;
    const servers = { ...this.servers() };
    if (this.editing && this.editing !== name) delete servers[this.editing];
    servers[name] = entry;
    await this.write({ ...this.doc, mcpServers: servers }, 'mcp-status');
    if (el('mcp-status').textContent?.startsWith('已保存')) closeDialog('mcp-dialog');
  }

  /** 切换某个服务器的启用状态。 */
  async toggle(name: string): Promise<void> {
    await this.load();
    const entry = this.servers()[name];
    if (!entry) { this.onError(new Error('服务器不存在，可能已被外部改动')); return; }
    const servers = { ...this.servers(), [name]: { ...entry, enabled: entry.enabled === false } };
    await this.write({ ...this.doc, mcpServers: servers });
  }

  /** 删除某个服务器。 */
  async remove(name: string): Promise<void> {
    await this.load();
    if (!this.servers()[name]) { this.onError(new Error('服务器不存在，可能已被外部改动')); return; }
    const servers = { ...this.servers() };
    delete servers[name];
    await this.write({ ...this.doc, mcpServers: servers });
  }

  /** 写回文档并刷新列表片段。statusId 非空时把结果写进该元素。 */
  private async write(doc: McpDoc, statusId = ''): Promise<void> {
    this.saving = true;
    const status = statusId ? el(statusId) : null;
    if (status) status.textContent = '正在保存…';
    try {
      await this.bridge.request('config.mcp.write', '', { document: doc }, 30_000);
      if (this.abort.signal.aborted) return;
      if (status) status.textContent = '已保存。';
      // 列表由桥渲染：写完触发一次刷新，让视图与磁盘一致。
      window.htmx.trigger(document.body, 'mcp-refresh');
    } catch (error) {
      if (this.abort.signal.aborted) return;
      this.onError(error);
      if (status) status.textContent = error instanceof Error ? error.message : '保存失败';
    } finally { this.saving = false; }
  }
}
