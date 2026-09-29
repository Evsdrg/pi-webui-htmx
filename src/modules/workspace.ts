// 工作区按需装载；关闭终端时同时释放服务端 PTY 与浏览器对象。
import type { BridgeClient } from './bridge';
import type { Message } from '@/types/protocol';
import { mountHighlight, languageFor } from './highlight';
import { record } from './stream';
import { el, elOrNull } from './dom';

/** 从事件目标向上找文件项。 */
function target_closest_file(target: Element): HTMLElement | null {
  return target.closest<HTMLElement>('[data-file-path]');
}

export class Workspace {
  private cwd = '';
  private path = '';
  private generation = 0;
  private terminal: import('./terminal').TerminalView | undefined;
  private abort = new AbortController();
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {
    // 点击监听挂在侧栏而不是右面板：文件树已经常驻侧栏下半，
    // 继续听 #workspace-panel 会让侧栏里的文件点击全部失效。
    // 两类目标分开处理：data-panel 只存在于右面板，data-file-path 只存在于侧栏，
    // 互不干扰，合成一个监听不会误判。
    el('sidebar').addEventListener('click', (event) => {
      const file = target_closest_file(event.target as Element);
      if (file) { event.preventDefault(); void (file.dataset.directory === 'true' ? this.list(file.dataset.filePath ?? '') : this.read(file.dataset.filePath ?? '')).catch(onError); return; }
      // 侧栏文件区的「上一级 / 刷新」也是 data-action：它们不在右面板里，
      // 只听 #workspace-panel 的话这两个按钮会是死的。
      const button = (event.target as Element).closest<HTMLElement>('[data-action]');
      if (button) void this.action(button.dataset.action ?? '').catch(onError);
    }, { signal: this.abort.signal });
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
      const button = target.closest<HTMLElement>('[data-action]');
      if (button) void this.action(button.dataset.action ?? '').catch(onError);
    }, { signal: this.abort.signal });
    this.mountSidebarFiles();
    bridge.addEventListener('disconnected', () => {
      this.terminal?.disconnected();
    }, { signal: this.abort.signal });
    // 迟到响应的守卫必须在 htmx 交换之前：等到 beforeSwap 之后才检查，
    // 旧目录/旧 diff 已经换进 DOM 了（U06）。
    //
    // 判定用「响应里的 path 是不是当前正在看的那一个」，而不是调用时的
    // 代次快照：声明式请求发起后 JS 不再持有它的句柄，而比对当前路径
    // 既能拦住迟到的旧响应，也不会拦错 A→B→A 这种回到同一目录的正常响应。
    document.addEventListener('htmx:beforeSwap', (event) => {
      const detail = (event as CustomEvent).detail as { target?: HTMLElement; xhr?: XMLHttpRequest; shouldSwap: boolean } | undefined;
      const target = detail?.target?.id;
      if (detail?.xhr && (target === 'file-list' || target === 'git-diff' || target === 'git-status')) {
        const requested = new URL(detail.xhr.responseURL, location.href).searchParams.get('path') ?? '';
        const current = target === 'file-list' ? this.path : this.cwd;
        if (requested !== current) detail.shouldSwap = false;
      }
    }, { signal: this.abort.signal });
    // 目录标签跟着实际落地的内容走：被守卫拒绝的响应不会触发 afterSwap。
    document.addEventListener('htmx:afterSwap', (event) => {
      const target = (event as CustomEvent).detail?.target as HTMLElement | undefined;
      if (target?.id === 'file-list') { const label = document.getElementById('sidebar-file-path'); if (label) label.textContent = this.path.split('/').filter(Boolean).pop() ?? ''; }
    }, { signal: this.abort.signal });
  }
  setCwd(cwd: string): void {
    if (cwd === this.cwd) return;
    this.generation++; this.cwd = cwd; this.path = cwd;
    el('file-list').replaceChildren(); el('git-status').replaceChildren(); el('git-diff').replaceChildren();
    if (this.terminal) void this.terminal.close().catch(this.onError);
    // 文件树常驻侧栏，它的 hx-trigger="load" 只在页面加载时触发过一次。
    // 切换会话后必须主动把新目录推过去，否则树还停在上一个项目的根上。
    const filesPath = document.getElementById('files-path') as HTMLInputElement | null;
    if (filesPath) filesPath.value = cwd;
    window.htmx.trigger(document.body, 'files-refresh');
  }
  async open(): Promise<void> {
    if (!this.cwd) { const data = await this.bridge.request<{roots:string[]}>('files.roots'); this.cwd = data.roots[0] ?? ''; }
    if (this.cwd) await this.list(this.path || this.cwd);
    else el('file-list').textContent = '桥没有配置可浏览的工作区。';
  }

  /** 侧栏文件区：折叠、分隔条拖动，以及目录标签回显。
      * 折叠只切 visibility，不卸载内容——文件列表是 htmx 拉的，
      * 收起来再展开不应重新请求一遍。 */
  private mountSidebarFiles(): void {
    const body = document.getElementById('sidebar-files-body');
    const section = document.getElementById('sidebar-files');
    const toggle = document.getElementById('files-collapse');
    const splitter = document.querySelector<HTMLElement>('.sidebar-splitter');
    if (!body || !section || !toggle) return;
    const readPref = (key: string) => { try { return localStorage.getItem(key); } catch { return null; } };
    const writePref = (key: string, value: string) => { try { localStorage.setItem(key, value); } catch { /* 隐私模式下忽略 */ } };
    const collapsed = readPref('sidebar-files-collapsed') === '1';
    this.applyFilesCollapsed(collapsed);
    toggle.addEventListener('click', () => {
      const next = body.hidden !== true;
      this.applyFilesCollapsed(next);
      writePref('sidebar-files-collapsed', next ? '1' : '0');
    }, { signal: this.abort.signal });
    // 分隔条拖动的是两区高度比例，写进 CSS 变量；
    // 与侧栏宽度同一手法——拖动期间由 layout.ts 加 data-resizing 抑制过渡。
    if (splitter) {
      let drag: { y: number; start: number } | null = null;
      const height = () => section.getBoundingClientRect().height;
      const setRatio = (value: number) => {
        section.style.setProperty('--files-ratio', String(value));
        splitter.setAttribute('aria-valuenow', String(Math.round(value * 100)));
      };
      // 比例写在 section 上而不是分隔条上：CSS 的 height 用的是
      // .sidebar-files { height: calc(var(--files-ratio) * 100%) }。
      // 读取与持久化都必须认准这一个元素，认错了会存进空串（第一版就是这样）。
      const currentRatio = () => Number(section.style.getPropertyValue('--files-ratio')) || 0.45;
      setRatio(Number(readPref('sidebar-files-ratio')) || 0.45);
      splitter.setAttribute('aria-valuemin', '15');
      splitter.setAttribute('aria-valuemax', '80');
      splitter.addEventListener('pointerdown', (event) => { drag = { y: event.clientY, start: currentRatio() }; splitter.setPointerCapture(event.pointerId); }, { signal: this.abort.signal });
      splitter.addEventListener('pointermove', (event) => {
        if (!drag) return;
        const total = height();
        if (total <= 0) return;
        setRatio(Math.min(0.8, Math.max(0.15, drag.start + (event.clientY - drag.y) / total)));
      }, { signal: this.abort.signal });
      const end = () => { if (!drag) return; drag = null; writePref('sidebar-files-ratio', String(currentRatio())); };
      splitter.addEventListener('pointerup', end, { signal: this.abort.signal });
      splitter.addEventListener('pointercancel', end, { signal: this.abort.signal });
      splitter.addEventListener('keydown', (event) => {
        if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
        event.preventDefault();
        const next = Math.min(0.8, Math.max(0.15, currentRatio() + (event.key === 'ArrowUp' ? -0.05 : 0.05)));
        setRatio(next); writePref('sidebar-files-ratio', String(next));
      }, { signal: this.abort.signal });
    }
  }

  private applyFilesCollapsed(collapsed: boolean): void {
    const body = document.getElementById('sidebar-files-body');
    const section = document.getElementById('sidebar-files');
    const toggle = document.getElementById('files-collapse');
    if (!body || !section || !toggle) return;
    body.hidden = collapsed;
    section.classList.toggle('is-collapsed', collapsed);
    toggle.setAttribute('aria-expanded', String(!collapsed));
    toggle.textContent = collapsed ? '▸' : '▾';
    toggle.title = collapsed ? '展开' : '折叠';
  }
  private async list(path: string): Promise<void> {
    // 更新动作全部交给 htmx：参数放在隐藏输入里，由 hx-include 带上去，
    // JS 不再拼 URL，也不再直接写 #file-list。
    this.generation++; this.path = path;
    el<HTMLInputElement>('files-path').value = path;
    window.htmx.trigger(document.body, 'files-refresh');
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
    // 完整内容走 HTTP：WS 是控制通道，单帧有上限，整份文本会把连接撑断（B07）。
    // HTTP 端点带压缩，大文件也更划算；只有它整体失败时才退回 WS 的截断预览。
    try {
      const response = await fetch(`/ui/file-text?path=${encodeURIComponent(path)}`, { credentials: 'same-origin' });
      if (!response.ok) throw new Error((await response.json().catch(() => null))?.error?.message ?? `读取失败（${response.status}）`);
      text = await response.text();
      if (response.headers.get('X-Truncated')) text += '\n[预览已截断]';
    } catch {
      try {
        const data = await this.bridge.request<{text:string;truncated:boolean}>('files.read', '', { path });
        text = data.text + (data.truncated ? '\n[预览已截断]' : '');
      } catch (error) {
        // 二进制文件：桥会给一句可读的原因，直接展示比静默失败好。
        this.showPreview(this.note(error instanceof Error ? error.message : '无法读取文件'));
        return;
      }
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
    mountHighlight(el('panel-preview'), languageFor(path));
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
    const box = el('panel-preview');
    const existing = box.querySelector('pre');
    if (existing) existing.remove();
    const pre = document.createElement('pre');
    pre.append(node);
    box.append(pre);
    box.hidden = false;
    // 预览在右面板：侧栏只有树。打开文件时把面板拉起来并切到预览 tab，
    // 否则内容换进了一个 hidden 的面板里，用户看到的是毫无变化。
    // 与 workbench 里切换工作区面板同一套写法：切 dataset.rightPanel 与 aria-expanded，
    // 不自己另搞一份状态。
    const tab = document.getElementById('tab-preview');
    const panel = el('workspace-panel');
    if (panel.hidden) {
      panel.hidden = false;
      el('workbench').dataset.rightPanel = 'open';
      document.querySelector<HTMLElement>('[aria-controls="workspace-panel"]')?.setAttribute('aria-expanded', 'true');
    }
    tab?.removeAttribute('hidden');
    tab?.click();
  }

  private async mountAnsi(): Promise<void> {
    try {
      const { mountAnsi } = await import('./ansi');
      mountAnsi(el('panel-preview'));
    } catch (error) { console.warn('ANSI 渲染失败', error); }
  }
  private async git(): Promise<void> {
    // 「变更」面板的两块内容都是「数据 → HTML」，全部交给桥渲染：
    // 状态行与文件列表走 /ui/git-status，差异走 /ui/diff。
    // 之前这里用 createElement 拼状态列表，截断文案因此重复了一份，
    // 而且和桥的服务端渲染容易走偏。
    const cwd = this.cwd;
    el<HTMLInputElement>('git-path').value = cwd;
    window.htmx.trigger(document.body, 'git-status-refresh');
    await this.diff(cwd);
  }
  // diff 是「变更」面板的更新动作：同样走 htmx 声明式请求。
  private async diff(cwd: string): Promise<void> {
    el<HTMLInputElement>('diff-path').value = cwd;
    window.htmx.trigger(document.body, 'diff-refresh');
  }
  private async action(action: string): Promise<void> {
    switch (action) {
      case 'files-up': { const parent = '/' + this.path.split('/').filter(Boolean).slice(0, -1).join('/'); await this.list(parent); break; }
      case 'files-refresh': await this.list(this.path || this.cwd); break;
      case 'file-close':
        // 图片预览用 <img> 顶掉了 #file-content，此时它已不在 DOM 里；
        // 直接 replaceChildren 会抛异常，关闭按钮就此失效（U12）。
        el('panel-preview').hidden = true;
        elOrNull('file-content')?.replaceChildren();
        break;
      case 'git-refresh': await this.git(); break;
      case 'terminal-open': if (!this.terminal) { const { TerminalView } = await import('./terminal'); this.terminal = new TerminalView(this.bridge, this.onError); } await this.terminal.open(this.cwd); break;
      case 'terminal-close': await this.terminal?.close(); break;
    }
  }
  event(message: Message): void { if (message.kind !== 'response') this.terminal?.event(message.event, record(message.data)); }
  dispose(): void { this.abort.abort(); this.terminal?.dispose(); }
}
