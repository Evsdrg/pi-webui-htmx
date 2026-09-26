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
  // read 按文件类型分流：图片走 <img>，其余走文本。
  //
  // 顺序是刻意的：先问桥「这是不是图片」。桥按魔数判断，比前端可靠；
  // 而且 files.read 现在会明确拒绝二进制，不会再像以前那样把 PNG 的
  // 字节转成 UTF-8 乱码返回。
  private async read(path: string): Promise<void> {
    const generation = ++this.generation;
    const image = await this.bridge.request<{mime:string;data:string}>('files.image', '', { path }).catch(() => null);
    if (generation !== this.generation) return;
    const name = path.split('/').pop() ?? path;
    el('file-name').textContent = name; el('file-name').title = path;
    if (image && image.data) {
      const frame = document.createElement('img');
      frame.className = 'file-image';
      frame.alt = name;
      frame.src = `data:${image.mime};base64,${image.data}`;
      this.showPreview(frame);
      return;
    }
    let text: string;
    try {
      const data = await this.bridge.request<{text:string;truncated:boolean}>('files.read', '', { path });
      text = data.text + (data.truncated ? '\n[预览已截断]' : '');
    } catch (error) {
      // 二进制文件：桥会给一句可读的原因，直接展示比静默失败好。
      this.showPreview(this.note(error instanceof Error ? error.message : '无法读取文件'));
      return;
    }
    const code = document.createElement('code');
    code.id = 'file-content';
    code.textContent = text;
    // 含 ANSI 转义时挂 .ansi，入口会用 ansi_up 着色；
    // 不挂就会被当成普通代码，转义序列原样显示。
    if (text.includes('\u001b[')) code.classList.add('ansi');
    this.showPreview(code);
    // ANSI 与语法高亮互斥：ansi_up 要按转义序列重新生成带色 span，
    // hljs 又会把同一段文本当代码再包一层。ANSI 文件只走前者。
    if (code.classList.contains('ansi')) { void this.mountAnsi(); return; }
    mountHighlight(el('file-preview'), languageFor(path));
  }

  private note(message: string): HTMLElement {
    const p = document.createElement('p');
    p.className = 'empty-note';
    p.textContent = message;
    return p;
  }

  // showPreview 用给定节点替换预览区内容。
  // 预览区从「只有一个 <pre><code>」变成可放任意节点，
  // 所以这里重建 <pre> 而不是复用固定结构。
  private showPreview(node: HTMLElement): void {
    const box = el('file-preview');
    const existing = box.querySelector('pre');
    if (existing) existing.remove();
    const pre = document.createElement('pre');
    pre.append(node);
    box.append(pre);
    box.hidden = false;
  }

  private async mountAnsi(): Promise<void> {
    try {
      const { mountAnsi } = await import('./ansi');
      mountAnsi(el('file-preview'));
    } catch (error) { console.warn('ANSI 渲染失败', error); }
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
