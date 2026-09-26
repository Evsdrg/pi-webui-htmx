// 工作区按需装载；关闭终端时同时释放服务端 PTY 与浏览器对象。
import type { BridgeClient } from './bridge';
import type { Message } from '@/types/protocol';
import { mountHighlight, languageFor } from './highlight';
import { record } from './stream';
const el = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

export class Workspace {
  private cwd = '';
  private path = '';
  private generation = 0;
  private terminal: import('./terminal').TerminalView | undefined;
  private abort = new AbortController();
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {
    el('workspace-panel').addEventListener('click', (event) => {
      const target = event.target as Element;
      const tab = target.closest<HTMLElement>('[data-panel]');
      if (tab) {
        for (const button of el('workspace-panel').querySelectorAll('[data-panel]')) button.setAttribute('aria-selected', String(button === tab));
        for (const panel of el('workspace-panel').querySelectorAll<HTMLElement>('[role=tabpanel]')) panel.hidden = panel.id !== `panel-${tab.dataset.panel}`;
        if (tab.dataset.panel === 'git') void this.git().catch(onError);
        this.terminal?.fit();
        return;
      }
      const file = target.closest<HTMLElement>('[data-file-path]');
      if (file) { event.preventDefault(); void (file.dataset.directory === 'true' ? this.list(file.dataset.filePath ?? '') : this.read(file.dataset.filePath ?? '')).catch(onError); return; }
      const button = target.closest<HTMLElement>('[data-action]');
      if (button) void this.action(button.dataset.action ?? '').catch(onError);
    }, { signal: this.abort.signal });
    bridge.addEventListener('disconnected', () => {
      this.terminal?.disconnected();
    }, { signal: this.abort.signal });
  }
  setCwd(cwd: string): void {
    if (cwd === this.cwd) return;
    this.generation++; this.cwd = cwd; this.path = cwd;
    el('file-list').replaceChildren(); el('file-preview').hidden = true; el('git-status').replaceChildren(); el('git-diff').replaceChildren();
    if (this.terminal) void this.terminal.close().catch(this.onError);
  }
  async open(): Promise<void> {
    if (!this.cwd) { const data = await this.bridge.request<{roots:string[]}>('files.roots'); this.cwd = data.roots[0] ?? ''; }
    if (this.cwd) await this.list(this.path || this.cwd);
    else el('file-list').textContent = '桥没有配置可浏览的工作区。';
  }
  private async list(path: string): Promise<void> {
    const generation = ++this.generation; this.path = path;
    await window.htmx.ajax('get', `/ui/files?path=${encodeURIComponent(path)}`, { target: '#file-list', swap: 'innerHTML' });
    if (generation !== this.generation) return;
    el('file-path').textContent = path;
  }
  private async read(path: string): Promise<void> {
    const generation = ++this.generation;
    const data = await this.bridge.request<{text:string;truncated:boolean;size:number}>('files.read', '', { path });
    if (generation !== this.generation) return;
    el('file-name').textContent = path.split('/').pop() ?? path; el('file-name').title = path;
    el('file-content').textContent = data.text + (data.truncated ? '\n[预览已截断]' : '');
    el('file-preview').hidden = false;
    mountHighlight(el('file-preview'), languageFor(path));
  }
  private async git(): Promise<void> {
    const cwd = this.cwd;
    const status = await this.bridge.request<{branch:string;clean:boolean;files:{path:string;status:string}[]}>('git.status', '', { path: cwd });
    if (cwd !== this.cwd) return;
    const list = el('git-status'); list.replaceChildren();
    const heading = document.createElement('p'); heading.textContent = `${status.branch} · ${status.clean ? '工作区干净' : `${status.files.length} 个变更`}`; list.append(heading);
    for (const file of status.files.slice(0, 500)) { const row = document.createElement('div'); row.className = 'file-item'; row.textContent = `${file.status}  ${file.path}`; list.append(row); }
    await window.htmx.ajax('get', `/ui/diff?path=${encodeURIComponent(cwd)}`, { target: '#git-diff', swap: 'innerHTML' });
  }
  private async action(action: string): Promise<void> {
    switch (action) {
      case 'files-up': { const parent = '/' + this.path.split('/').filter(Boolean).slice(0, -1).join('/'); await this.list(parent); break; }
      case 'files-refresh': await this.list(this.path || this.cwd); break;
      case 'file-close': el('file-preview').hidden = true; el('file-content').replaceChildren(); break;
      case 'git-refresh': await this.git(); break;
      case 'terminal-open': if (!this.terminal) { const { TerminalView } = await import('./terminal'); this.terminal = new TerminalView(this.bridge, this.onError); } await this.terminal.open(this.cwd); break;
      case 'terminal-close': await this.terminal?.close(); break;
    }
  }
  event(message: Message): void { if (message.kind !== 'response') this.terminal?.event(message.event, record(message.data)); }
  dispose(): void { this.abort.abort(); this.terminal?.dispose(); }
}
