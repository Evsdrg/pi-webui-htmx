// 工作区按需装载；关闭终端时同时释放服务端 PTY 与浏览器对象。
import type { BridgeClient } from './bridge';
import type { Message } from '@/types/protocol';
import { mountHighlight, languageFor } from './highlight';
import { record } from './stream';
import { el, elOrNull } from './dom';
import { readPreference, savePreference } from './layout';

/** 从事件目标向上找文件项。 */
function target_closest_file(target: Element): HTMLElement | null {
  return target.closest<HTMLElement>('[data-file-path]');
}

export class Workspace {
  private cwd = '';
  private path = '';
  private generation = 0;
  private previewRequest: AbortController | undefined;
  /** 当前预览图片的 blob URL；换预览时释放。 */
  private imageUrl: string | undefined;
  private terminal: import('./terminal').TerminalView | undefined;
  private abort = new AbortController();
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {
    // 点击监听挂在侧栏而不是右面板：文件树已经常驻侧栏下半，
    // 继续听 #workspace-panel 会让侧栏里的文件点击全部失效。
    // 两类目标分开处理：data-panel 只存在于右面板，data-file-path 只存在于侧栏，
    // 互不干扰，合成一个监听不会误判。
    el('sidebar').addEventListener('click', (event) => {
      const file = target_closest_file(event.target as Element);
      // 目录点击是「就地展开」不是「进入」：根目录只由上一级/刷新/会话切换改变，
      // 子层展开见 toggleDirectory（与刷新共用同一条片段端点）。
      if (file) { event.preventDefault(); void (file.dataset.directory === 'true' ? this.toggleDirectory(file) : this.read(file.dataset.filePath ?? '')).catch(onError); return; }
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
        // 还没有「当前目录」时不判定：这种请求是外部发起的（workbench 的启动
        // 恢复、HTML 里的 hx-trigger），path 为空只说明 workspace 还没同步过。
        // 拦掉它会让文件树在刷新页面后永远停在空提示上——只有当 workspace
        // 确有当前目录、而这个响应不属于它时，才是真正迟到的旧响应（U06）。
        if (current && requested !== current) detail.shouldSwap = false;
      }
    }, { signal: this.abort.signal });
    // 目录标签跟着实际落地的内容走：被守卫拒绝的响应不会触发 afterSwap。
    document.addEventListener('htmx:afterSwap', (event) => {
      const target = (event as CustomEvent).detail?.target as HTMLElement | undefined;
      if (target?.id === 'file-list') { const label = document.getElementById('sidebar-file-path'); if (label) label.textContent = this.path.split('/').filter(Boolean).pop() ?? ''; }
    }, { signal: this.abort.signal });
    // 状态片段的结果到达后决定差异区：成功才请求并显示；失败（非 git 仓库等）
    // 保持隐藏——错误已在状态区显示一次，重复展示是同一句话出现两遍的来源。
    document.addEventListener('htmx:afterRequest', (event) => {
      const detail = (event as CustomEvent).detail as { elt?: Element; xhr?: { status?: number; responseText?: string } } | undefined;
      if (detail?.elt instanceof Element && detail.elt.id === 'git-status') this.gitStatusLoaded(detail.xhr);
    }, { signal: this.abort.signal });
  }
  setCwd(cwd: string): void {
    if (cwd === this.cwd) return;
    this.generation++; this.previewRequest?.abort(); this.cwd = cwd; this.path = cwd;
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
    // 偏好走 layout.ts 的 pi-ui: 命名空间；这两个键早期以无前缀的裸键存在过，
    // 读不到新键时回退旧键并顺手迁移，已存过折叠状态/比例的浏览器不至于丢。
    const readFilesPref = (key: string) => {
      const value = readPreference(key);
      if (value) return value;
      let legacy: string | null = null;
      try { legacy = localStorage.getItem(key); } catch { /* 隐私模式下按未设置处理 */ }
      if (legacy) savePreference(key, legacy);
      return legacy ?? '';
    };
    const collapsed = readFilesPref('sidebar-files-collapsed') === '1';
    this.applyFilesCollapsed(collapsed);
    toggle.addEventListener('click', () => {
      const next = body.hidden !== true;
      this.applyFilesCollapsed(next);
      savePreference('sidebar-files-collapsed', next ? '1' : '0');
    }, { signal: this.abort.signal });
    // 分隔条拖动的是两区高度比例，写进 CSS 变量，由 .sidebar-files 的
    // height:calc(var(--files-ratio) * 100%) 消费；拖动期间一帧只写一次样式。
    if (splitter) {
      // 文件区最小高度：分隔条 + 标题栏 + 至少一行文件。
      const MIN_FILES_H = 64;
      // 会话区的最小高度必须与 CSS 里 #session-list 的 min-height 一致：
      // 拖到极限时边界停住，是因为布局真的到极限了。
      const MIN_SESSIONS_H = 60;
      let drag: { y: number; h0: number; total: number; maxH: number } | null = null;
      let pendingY: number | null = null;
      let frame = 0;

      const setRatio = (value: number) => {
        const ratio = Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 0.45;
        section.style.setProperty('--files-ratio', String(ratio));
        splitter.setAttribute('aria-valuenow', String(Math.round(ratio * 100)));
      };
      const currentRatio = () => Number(section.style.getPropertyValue('--files-ratio')) || 0.45;
      // 可达范围按实测几何算：文件区最多长到「会话区只剩 MIN_SESSIONS_H」为止。
      // 旧版钳在固定比例 [0.15, 0.8]，落在到不了的位置时会被 flex 压缩，
      // 边界就不再跟鼠标走。
      const limits = () => {
        const parent = section.parentElement;
        if (!parent) return null;
        const style = getComputedStyle(parent);
        const total = parent.getBoundingClientRect().height
          - (parseFloat(style.paddingTop) || 0) - (parseFloat(style.paddingBottom) || 0);
        if (!(total > 0)) return null;
        const h0 = section.getBoundingClientRect().height;
        const sessions = document.getElementById('session-list')?.getBoundingClientRect().height ?? 0;
        const maxH = Math.min(total, Math.max(MIN_FILES_H, h0 + Math.max(0, sessions - MIN_SESSIONS_H)));
        return { total, h0, maxH };
      };

      setRatio(Number(readFilesPref('sidebar-files-ratio')) || 0.45);
      splitter.setAttribute('aria-valuemin', '0');
      splitter.setAttribute('aria-valuemax', '100');

      const apply = () => {
        frame = 0;
        const y = pendingY; pendingY = null;
        if (y === null || !drag) return;
        const height = Math.min(drag.maxH, Math.max(MIN_FILES_H, drag.h0 - (y - drag.y)));
        setRatio(height / drag.total);
      };
      splitter.addEventListener('pointerdown', (event) => {
        if (drag) return;
        // 折叠态由 height:auto 接管，拖动没有意义。
        if (section.classList.contains('is-collapsed')) return;
        const bounds = limits();
        if (!bounds) return;
        drag = { y: event.clientY, h0: bounds.h0, total: bounds.total, maxH: bounds.maxH };
        splitter.setPointerCapture(event.pointerId);
      }, { signal: this.abort.signal });
      // 每个 pointermove 都写样式会连环触发重排，一帧只应用最后一次（与 scroll.ts 同一手法）。
      splitter.addEventListener('pointermove', (event) => {
        if (!drag) return;
        pendingY = event.clientY;
        if (!frame) frame = requestAnimationFrame(apply);
      }, { signal: this.abort.signal });
      const end = () => {
        if (!drag) return;
        if (frame) { cancelAnimationFrame(frame); frame = 0; }
        apply(); // 收尾一帧要应用完，边界不落在半路
        drag = null;
        savePreference('sidebar-files-ratio', String(currentRatio()));
      };
      splitter.addEventListener('pointerup', end, { signal: this.abort.signal });
      splitter.addEventListener('pointercancel', end, { signal: this.abort.signal });
      splitter.addEventListener('lostpointercapture', end, { signal: this.abort.signal });
      splitter.addEventListener('keydown', (event) => {
        if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
        event.preventDefault();
        const bounds = limits();
        if (!bounds) return;
        const step = 0.05 * bounds.total;
        const height = Math.min(bounds.maxH, Math.max(MIN_FILES_H, bounds.h0 + (event.key === 'ArrowUp' ? step : -step)));
        setRatio(height / bounds.total);
        savePreference('sidebar-files-ratio', String(currentRatio()));
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
    this.generation++; this.previewRequest?.abort(); this.path = path;
    el('file-list').dataset.requestScope = String(this.generation);
    el<HTMLInputElement>('files-path').value = path;
    window.htmx.trigger(document.body, 'files-refresh');
  }

  // 目录就地展开：子层挂在该节点的 .file-children 里，根目录不变。
  // 只有首次展开才拉片段，之后只是显隐——反复开合不该反复打服务端；
  // 与根列表共用同一条渲染路径（/ui/files），前端不掌握列表结构。
  private async toggleDirectory(button: HTMLElement): Promise<void> {
    const children = button.closest('.file-node')?.querySelector<HTMLElement>('.file-children');
    if (!children) return;
    if (button.getAttribute('aria-expanded') === 'true') {
      button.setAttribute('aria-expanded', 'false');
      children.hidden = true;
      return;
    }
    button.setAttribute('aria-expanded', 'true');
    children.hidden = false;
    // loading 期间连点不重复请求；失败时清掉状态，收起再展开可重试。
    if (children.dataset.state) return;
    children.dataset.state = 'loading';
    children.replaceChildren(this.note('载入中…'));
    try {
      await window.htmx.ajax('get', `ui/files?path=${encodeURIComponent(button.dataset.filePath ?? '')}`, { target: children, swap: 'innerHTML' });
      children.dataset.state = 'loaded';
    } catch {
      delete children.dataset.state;
      children.replaceChildren(this.note('目录读取失败，收起后重试。'));
    }
  }
  // read 按文件类型分流：图片走 <img>，其余走文本。
  //
  // 两条分支都走 HTTP。WS 是控制通道、单帧上限 512 KiB：文本曾经因此
  // 被撑断连接（B07），图片则更直接——图片允许到 4 MiB，整帧会被连接层
  // 丢弃，命令只会让用户看到超时（B33）。HTTP 端点回原始字节，不需要
  // base64 展开，还能让浏览器自己解码与缓存。
  private async read(path: string): Promise<void> {
    this.previewRequest?.abort();
    const controller = new AbortController(); this.previewRequest = controller;
    const generation = ++this.generation;
    const alive = () => !this.abort.signal.aborted && !controller.signal.aborted && generation === this.generation;
    const name = path.split('/').pop() ?? path;
    el('file-name').textContent = name; el('file-name').title = path;
    const image = await this.fetchImage(path, controller);
    if (!alive()) { if (image) URL.revokeObjectURL(image.url); return; }
    if (image) { this.showPreview(image.node, image.url); return; }
    let text: string;
    // 完整内容走 HTTP：WS 是控制通道，单帧有上限，整份文本会把连接撑断（B07）。
    // HTTP 端点带压缩，大文件也更划算；只有它整体失败时才退回 WS 的截断预览。
    try {
      const response = await fetch(`ui/file-text?path=${encodeURIComponent(path)}`, { credentials: 'same-origin', signal: controller.signal });
      if (!response.ok) throw new Error((await response.json().catch(() => null))?.error?.message ?? `读取失败（${response.status}）`);
      text = await response.text();
      if (response.headers.get('X-Truncated')) text += '\n[预览已截断]';
    } catch {
      if (!alive()) return;
      try {
        const data = await this.bridge.request<{text:string;truncated:boolean}>('files.read', '', { path });
        text = data.text + (data.truncated ? '\n[预览已截断]' : '');
      } catch (error) {
        // 二进制文件：桥会给一句可读的原因，直接展示比静默失败好。
        if (!alive()) return;
        this.showPreview(this.note(error instanceof Error ? error.message : '无法读取文件'));
        return;
      }
    }
    if (!alive()) return;
    const code = document.createElement('code');
    code.id = 'file-content';
    code.textContent = text;
    // 含 ANSI 转义时挂 .ansi，入口会用 ansi_up 着色；
    // 不挂就会被当成普通代码，转义序列原样显示。
    if (text.includes('\u001b[')) code.classList.add('ansi');
    // 散文（Markdown / 纯文本 / 扩展名未收录的文本）软换行；代码与数据横向滚动。
    // languageFor 对 .md 返回 "markdown"、对未收录扩展名返回空串。
    const lang = languageFor(path);
    const wrap = lang === '' || lang === 'markdown';
    this.showPreview(code, undefined, wrap);
    // ANSI 与语法高亮互斥：ansi_up 要按转义序列重新生成带色 span，
    // hljs 又会把同一段文本当代码再包一层。ANSI 文件只走前者。
    if (code.classList.contains('ansi')) { void this.mountAnsi(); return; }
    mountHighlight(el('panel-preview'), lang);
  }

  private note(message: string): HTMLElement {
    const p = document.createElement('p');
    p.className = 'empty-note';
    p.textContent = message;
    return p;
  }

  // releaseImageUrl 释放当前图片预览的 blob URL。换图已走 showPreview，
  // 但关闭预览与销毁工作区以前只清 DOM，对象 URL 留到页面卸载（R01）。
  private releaseImageUrl(): void {
    if (!this.imageUrl) return;
    URL.revokeObjectURL(this.imageUrl);
    this.imageUrl = undefined;
  }

  // fetchImage 取图片预览。不是图片、读取失败或已取消时返回 null，
  // 调用方据此回退到文本分支——不需要额外的「这是不是图片」往返。
  private async fetchImage(path: string, controller: AbortController): Promise<{ node: HTMLImageElement; url: string } | null> {
    try {
      const response = await fetch(`ui/file-image?path=${encodeURIComponent(path)}`, { credentials: 'same-origin', signal: controller.signal });
      if (!response.ok) return null;
      const url = URL.createObjectURL(await response.blob());
      const node = document.createElement('img');
      node.className = 'file-image';
      node.alt = path.split('/').pop() ?? path;
      node.src = url;
      return { node, url };
    } catch { return null; }
  }

  // showPreview 用给定节点替换预览区内容。
  // 预览区从「只有一个 <pre><code>」变成可放任意节点，
  // 所以这里重建 <pre> 而不是复用固定结构。
  //
  // imageUrl 是这张图对应的 blob URL：换预览时旧的要释放，否则每看一张图
  // 都留一份解码后的位图。释放放在旧节点已从 DOM 摘除之后。
  // wrap 为真时给 <pre> 挂 .wrap，用软换行替代横向滚动（散文类文件）。
  private showPreview(node: HTMLElement, imageUrl?: string, wrap = false): void {
    const box = el('panel-preview');
    const existing = box.querySelector('pre');
    if (existing) existing.remove();
    if (this.imageUrl && this.imageUrl !== imageUrl) this.releaseImageUrl();
    this.imageUrl = imageUrl;
    const pre = document.createElement('pre');
    if (wrap) pre.className = 'wrap';
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
      if (this.abort.signal.aborted) return;
      mountAnsi(el('panel-preview'));
    } catch (error) { console.warn('ANSI 渲染失败', error); }
  }
  private async git(): Promise<void> {
    // 「变更」面板的两块内容都是「数据 → HTML」，全部交给桥渲染：
    // 状态行与文件列表走 /ui/git-status，差异走 /ui/diff。
    // 但两者不能同时触发：非 git 仓库时两个端点渲染的是同一句错误，
    // 界面上会出现两次。先请求状态，由 gitStatusLoaded 按结果决定差异区。
    const cwd = this.cwd;
    el<HTMLInputElement>('git-path').value = cwd;
    // 刷新期间先藏旧差异，避免它与新状态短暂矛盾；结果到达后由
    // gitStatusLoaded 决定显示（成功）还是保持隐藏（失败）。
    const diff = document.getElementById('git-diff');
    if (diff) diff.hidden = true;
    window.htmx.trigger(document.body, 'git-status-refresh');
  }
  // gitStatusLoaded 处理状态片段的结果：只有成功（非 .empty-note）才请求
  // 差异并显示；失败时错误已由状态区呈现一次，差异区保持隐藏。
  private gitStatusLoaded(xhr: { status?: number; responseText?: string } | undefined): void {
    const diff = document.getElementById('git-diff');
    if (!diff) return;
    const failed = !xhr || xhr.status !== 200 || (xhr.responseText ?? '').includes('empty-note');
    diff.hidden = failed;
    if (!failed) void this.diff(this.cwd);
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
        this.generation++; this.previewRequest?.abort();
        this.releaseImageUrl();
        // 图片预览用 <img> 顶掉了 #file-content，此时它已不在 DOM 里；
        // 直接 replaceChildren 会抛异常，关闭按钮就此失效（U12）。
        el('panel-preview').hidden = true;
        elOrNull('file-content')?.replaceChildren();
        break;
      case 'git-refresh': await this.git(); break;
      case 'terminal-open': if (!this.terminal) { const { TerminalView } = await import('./terminal'); if (this.abort.signal.aborted) return; this.terminal = new TerminalView(this.bridge, this.onError); } await this.terminal.open(this.cwd); break;
      case 'terminal-close': await this.terminal?.close(); break;
    }
  }
  event(message: Message): void { if (message.kind !== 'response') this.terminal?.event(message.event, record(message.data)); }
  dispose(): void { this.generation++; this.previewRequest?.abort(); this.releaseImageUrl(); this.abort.abort(); this.terminal?.dispose(); }
}
