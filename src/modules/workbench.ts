import { BridgeClient, BridgeError } from './bridge';
import { LiveView, EventCursor, record, text, runStateAfter, type RunState } from './stream';
import { closeMobileSidebar, readDraft, saveDraft } from './layout';
import { addFiles, toWire, formatSize } from './attachments';
import type { Attachment } from './attachments';
import type { Capabilities, EventMessage, Message, Method, WorkerInfo } from '@/types/protocol';
import { closeDialog, el, openDialog } from './dom';
import { SessionScope } from './scope';
import type { Scope } from './scope';

const DIALOGS = new Set(['select','confirm','input','editor']);
interface State { sessionId: string; sessionName?: string; isStreaming: boolean; isCompacting: boolean; thinkingLevel?: string; model?: { id: string; provider: string; name: string }; pendingMessageCount?: number; steeringMode?: string; followUpMode?: string; autoCompactionEnabled?: boolean }

export class Workbench {
  readonly bridge = new BridgeClient(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/ws`);
  private capabilities = new Set<Method>();
  private abort = new AbortController();
  private sessionId = document.body.dataset.sessionId ?? '';
  /**
   * 会话作用域。切会话时代次递增，所有在途凭证立即失效——
   * 这是「等待期间切会话不要把操作投到新会话」的唯一依据（U03/U13）。
   */
  private scope = new SessionScope(this.sessionId);
  private cwd = '';
  private run: RunState = 'idle';
  private sending = false;
  private diskSession = !!this.sessionId;
  private cursor = new EventCursor();
  private live = new LiveView(el('live'));
  private attachments: Attachment[] = [];
  /** 附件批次的串行队列，见 attach 的说明（U20）。 */
  private attachQueue: Promise<void> = Promise.resolve();
  private statuses = new Map<string, string>();
  private widgets = new Map<string, { lines: string[]; placement: string }>();
  private commands: { name: string; description: string }[] = [];
  private poll: ReturnType<typeof setInterval> | undefined;
  private searchTimer: ReturnType<typeof setTimeout> | undefined;
  private historyLoading = '';
  private subscribed = '';
  private dialogsLoading = false;
  private dialogsDirty = false;
  private workspace: import('./workspace').Workspace | undefined;
  private models: import('./models').ModelsEditor | undefined;
  private branch: import('./branch').BranchNavigator | undefined;
  private mention: import('./mention').FileCompleter | undefined;
  private currentModel: { provider: string; id: string; name: string } | undefined;

  constructor(private readonly bottom: () => void) {}
  start(): void {
    const signal = this.abort.signal;
    this.bridge.addEventListener('connected', () => { this.setConnection(true); void this.reconcile().catch((err) => this.fail(err)); }, { signal });
    this.bridge.addEventListener('disconnected', () => { this.subscribed = ''; this.setConnection(false); this.notice('连接已断开，正在重连。已提交的任务继续在本地运行；不会自动重发命令。'); }, { signal });
    this.bridge.addEventListener('message', (event) => this.onMessage((event as CustomEvent<Message>).detail), { signal });
    el('auth-form').addEventListener('submit', (event) => { event.preventDefault(); void this.login(); }, { signal });
    el('composer').addEventListener('submit', (event) => { event.preventDefault(); void this.send(); }, { signal });
    // 单个 keydown 处理器，@ 菜单优先。
    // 曾经这里是两个监听：第一个无条件 requestSubmit()，第二个才想
    // preventDefault()——那时表单已经提交，半个查询（"@"）就被当成
    // 消息发出去了。合并不但修掉这个顺序问题，也避免再长出第三个。
    el<HTMLTextAreaElement>('prompt').addEventListener('keydown', (event) => {
      if (this.mention?.active) {
        if (event.key === 'ArrowDown') { event.preventDefault(); this.mention.move(1); return; }
        if (event.key === 'ArrowUp') { event.preventDefault(); this.mention.move(-1); return; }
        // Enter/Tab 一律消费：没有候选也绝不放行，否则 "@" 会被提交。
        if (event.key === 'Enter' || event.key === 'Tab') { this.mention.choose(); event.preventDefault(); return; }
        if (event.key === 'Escape') { event.preventDefault(); this.mention.hide(); return; }
      }
      if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); el<HTMLFormElement>('composer').requestSubmit(); }
    }, { signal });
    el<HTMLTextAreaElement>('prompt').addEventListener('input', () => {
      this.saveCurrentDraft(); this.showCommands();
      // @ 补全是增强功能：它出任何问题都不能挡住 updateControls，
      // 否则发送按钮与排队提示会停在一个错误状态上。
      try { this.mention?.refresh(); } catch (error) { console.warn('@ 补全刷新失败', error); }
      this.updateControls();
    }, { signal });
    el('new-form').addEventListener('submit', (event) => {
      event.preventDefault(); const cwd = el<HTMLInputElement>('cwd-input').value.trim();
      if (!cwd) return; this.selectSession('', cwd, '新会话'); closeDialog('new-dialog'); el('prompt').focus();
    }, { signal });
    el('session-search').addEventListener('input', () => {
      clearTimeout(this.searchTimer); const query = el<HTMLInputElement>('session-search').value.trim();
      this.searchTimer = setTimeout(() => void this.search(query), 250);
    }, { signal });
    el('model-select').addEventListener('change', () => void this.changeModel().catch((err) => this.fail(err)), { signal });
    el('auto-compaction').addEventListener('change', () => {
      const enabled = el<HTMLInputElement>('auto-compaction').checked;
      // 成功后必须回读：command() 会先ensureWorker，其中的预取状态刷新
      // 发生在 set 之前，会把勾选重置成旧值。回读才能反映 Pi 的真实状态。
      void this.command('session.set_auto_compaction', { enabled })
        .then(async () => { this.notify(enabled ? '已开启自动压缩。' : '已关闭自动压缩。'); await this.refreshState(); })
        .catch((err) => { this.fail(err); void this.refreshState(); });
    }, { signal });
    this.wireAttachments();
    void this.wireMention();
    void this.wireLazy();
    el('auto-retry').addEventListener('change', () => {
      const enabled = el<HTMLInputElement>('auto-retry').checked;
      // Pi 没有自动重试的读回字段，失败时把勾选还原，避免界面停在假状态。
      void this.command('session.set_auto_retry', { enabled }).then(() => this.notify(enabled ? '已开启自动重试。' : '已关闭自动重试。')).catch((err) => { this.fail(err); el<HTMLInputElement>('auto-retry').checked = !enabled; });
    }, { signal });
    el('thinking-select').addEventListener('change', () => { const level = el<HTMLSelectElement>('thinking-select').value; void this.command('session.set_thinking', { level }).catch((err) => this.fail(err)); }, { signal });
    document.addEventListener('click', (event) => this.onClick(event), { signal });
    document.addEventListener('keydown', (event) => { if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); void this.newSession().catch((err) => this.fail(err)); } }, { signal });
    window.addEventListener('popstate', () => this.selectSession(new URL(location.href).searchParams.get('session') ?? '', '', '会话', false), { signal });
    document.addEventListener('visibilitychange', () => { if (!document.hidden && this.bridge.connected) void this.reconcile().catch((err) => this.fail(err)); }, { signal });
    document.addEventListener('htmx:beforeSwap', (event) => {
      const detail = (event as CustomEvent).detail as { target?: HTMLElement; xhr?: XMLHttpRequest; shouldSwap: boolean };
      const url = detail.xhr?.responseURL;
      if (!url) return;
      const response = new URL(url);
      if (detail.target?.id === 'turns' && response.pathname !== `/ui/sessions/${encodeURIComponent(this.sessionId)}/history`) detail.shouldSwap = false;
      if (detail.target?.id === 'ext-dialog-slot' && response.searchParams.get('sessionId') !== this.sessionId) detail.shouldSwap = false;
    }, { signal });
    document.addEventListener('htmx:afterSwap', (event) => {
      const target = (event as CustomEvent).detail?.target as HTMLElement | undefined;
      if (target?.id === 'session-list') this.markSelected();
      if (target?.id === 'ext-dialog-slot') this.openExtensionDialog();
      // 模型片段是异步到达的，可能晚于 reconcile 动态补出的当前模型选项；
      // 换入后必须重新应用，否则下拉会回落到「默认模型」。
      if (target?.id === 'model-select') { const model = this.currentModel; if (model) this.selectModel(model.provider, model.id, model.name); }
    }, { signal });
    document.addEventListener('htmx:responseError', (event) => {
      const xhr = (event as CustomEvent).detail?.xhr as XMLHttpRequest | undefined;
      if (xhr?.status === 401) { this.showLogin(); return; }
      if (xhr) { let message = `请求失败（${xhr.status}）`; try { message = text(record(record(JSON.parse(xhr.responseText)).error).message) || message; } catch { /* 保留状态码。 */ } this.fail(new Error(message)); }
    }, { signal });
    document.addEventListener('htmx:sendError', () => this.fail(new Error('无法读取页面内容，请检查桥的连接。')), { signal });
    document.addEventListener('submit', (event) => {
      const form = event.target as HTMLFormElement;
      if (form.matches('[data-extension-form]')) { event.preventDefault(); void this.answerDialog(form, (event as SubmitEvent).submitter as HTMLButtonElement | null); }
    }, { signal });
    // 低频核对可修复后台标签丢失的结束事件；不访问未运行的 Pi。
    this.poll = setInterval(() => { if (!document.hidden && this.bridge.connected && this.run !== 'idle') void this.reconcile().catch((err) => this.fail(err)); }, 15_000);
    el<HTMLTextAreaElement>('prompt').value = readDraft(this.draftKey());
    void this.authenticate();
  }

  private async authenticate(): Promise<void> {
    try {
      const response = await fetch('/api/v1/capabilities', { cache: 'no-store' });
      if (response.status === 401) { this.showLogin(); return; }
      if (!response.ok) throw new Error(`读取桥能力失败（${response.status}）`);
      const caps = await response.json() as Capabilities;
      if (caps.version !== 1 || !Array.isArray(caps.methods) || !['session.start','session.prompt','session.subscribe'].every((method) => caps.methods.includes(method as Method))) throw new Error('桥的协议或核心能力不兼容');
      this.capabilities = new Set(caps.methods);
      closeDialog('auth-dialog');
      this.refreshSessions(); window.htmx.trigger(document.body, 'models-refresh');
      if (this.sessionId) { el('welcome').hidden = true; el('chat-scroll').dataset.resetScroll = 'true'; void this.refreshHistory(); }
      this.bridge.connect();
    } catch (error) { this.fail(error); this.setConnection(false); }
  }
  private showLogin(): void { openDialog('auth-dialog'); }
  private async login(): Promise<void> {
    const input = el<HTMLInputElement>('bridge-token');
    const button = el<HTMLButtonElement>('auth-form').querySelector('button')!;
    button.disabled = true; el('auth-error').textContent = '';
    try {
      const response = await fetch('/api/v1/auth', { method: 'POST', headers: { Authorization: `Bearer ${input.value}` } });
      if (!response.ok) throw new Error(response.status === 401 ? '令牌无效，请重试。' : `登录失败（${response.status}）`);
      input.value = ''; await this.authenticate();
    } catch (error) { el('auth-error').textContent = error instanceof Error ? error.message : '登录失败'; }
    finally { button.disabled = false; }
  }
  private request<T = unknown>(method: Method, params?: unknown, session = this.sessionId): Promise<T> {
    if (!this.capabilities.has(method)) return Promise.reject(new BridgeError('unsupported_method', `当前桥不支持 ${method}`));
    return this.bridge.request<T>(method, session, params, method === 'session.compact' ? 120_000 : 30_000);
  }
  private async ensureWorker(scope: Scope = this.scope.current$()): Promise<WorkerInfo> {
    const oldId = this.sessionId;
    if (!oldId && !this.cwd) { await this.newSession(); throw new Error('请先选择工作目录，再发送消息。'); }
    const info = await this.request<WorkerInfo>('session.start', { cwd: this.cwd }, oldId);
    if (!scope.alive()) throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
    if (!oldId) {
      // 新会话 ID 由 worker 分配，必须经 scope 提交，让后续操作都归属新会话。
      this.scope.switchTo(info.sessionId);
      this.sessionId = info.sessionId; this.cwd = info.cwd;
      document.body.dataset.sessionId = this.sessionId;
      history.replaceState(null, '', `/?session=${encodeURIComponent(info.sessionId)}`);
    }
    this.cwd = info.cwd; el('session-cwd').textContent = this.cwd;
    await this.subscribe();
    // 刚启动的 worker 才知道当前模型；不刷新会让下拉停留在「默认模型」，
    // 与 Pi 实际使用的模型不一致。
    await this.refreshState(scope);
    return info;
  }
  /**
   * command 把目标会话固定为发起时那一个。
   *
   * 以前是 await ensureWorker() 之后再用 this.request 的默认参数读
   * this.sessionId——等待期间切会话，操作就落到了新会话上（U03）。
   * 现在 target 在发起时取定，整条链路都显式传它。
   */
  private async command<T = unknown>(method: Method, params?: unknown, scope: Scope = this.scope.current$()): Promise<T> {
    await this.ensureWorker(scope);
    return this.request<T>(method, params, scope.sessionId);
  }
  // refreshState 读取一次会话状态并同步界面。
  // 只按会话作用域守卫：状态读取发生在已处理的事件之后，内容是更新的；
  // 若再用 seq 比较，一流式输出就会让刷新永远放弃，模型下拉停留在占位项。
  private async refreshState(scope: Scope = this.scope.current$()): Promise<void> {
    let state: State;
    try { state = await this.request<State>('session.state'); } catch { return; }
    if (!scope.alive()) return;
    if (state.sessionName) el('session-title').textContent = state.sessionName;
    if (state.model) { this.currentModel = { provider: state.model.provider, id: state.model.id, name: state.model.name }; this.selectModel(state.model.provider, state.model.id, state.model.name); }
    await this.refreshThinking(state.thinkingLevel);
    this.refreshQueueState(state);
  }
  private async subscribe(): Promise<void> {
    const id = this.sessionId; if (!id || this.subscribed === id) return;
    const saved = this.cursor.epoch ? { epoch: this.cursor.epoch, afterSeq: this.cursor.seq } : {};
    try { await this.request('session.subscribe', saved, id); } catch (error) {
      if (!(error instanceof BridgeError) || error.code !== 'resync_required') throw error;
      if (id !== this.sessionId) return;
      this.cursor.reset(); this.live.clear();
      this.notice('实时事件已超出补发窗口，已重新读取历史；生成中的缺失内容将在本轮完成后同步。');
      await this.refreshHistory(); await this.request('session.subscribe', {}, id);
    }
    if (id === this.sessionId) this.subscribed = id;
  }
  private async reconcile(): Promise<void> {
    const scope = this.scope.current$();
    const workers = await this.request<WorkerInfo[]>('worker.list', undefined, '');
    if (!scope.alive()) return;
    const worker = workers.find((w) => w.sessionId === this.sessionId);
    if (!worker) { this.setRun('idle'); return; }
    this.cwd = worker.cwd; el('session-cwd').textContent = this.cwd;
    await this.subscribe();
    const stateSeq = this.cursor.seq;
    const state = await this.request<State>('session.state');
    if (!scope.alive() || stateSeq !== this.cursor.seq) return;
    const wasBusy = this.run !== 'idle';
    const busy = state.isStreaming || state.isCompacting || (state.pendingMessageCount ?? 0) > 0;
    this.setRun(busy ? (state.isCompacting ? 'compacting' : 'running') : 'idle');
    if (state.sessionName) el('session-title').textContent = state.sessionName;
    if (state.model) { this.currentModel = { provider: state.model.provider, id: state.model.id, name: state.model.name }; this.selectModel(state.model.provider, state.model.id, state.model.name); }
    if (wasBusy && !busy) await this.settled();
    await this.refreshThinking(state.thinkingLevel);
    this.refreshQueueState(state);
    await this.refreshDialogs();
  }
  private selectSession(id: string, cwd: string, title: string, push = true): void {
    this.saveCurrentDraft(); const previous = this.sessionId; this.scope.switchTo(id);
    if (previous && this.bridge.connected) void this.request('session.unsubscribe', undefined, previous).catch(() => {});
    this.sessionId = id; this.subscribed = ''; this.cwd = cwd; this.diskSession = !!id; this.cursor.reset(); this.live.clear();
    this.statuses.clear(); this.widgets.clear(); this.renderExtensions(); this.commands = [];
    el('turns').replaceChildren(); el('older-slot').replaceChildren(); el('ext-dialog-slot').replaceChildren();
    el('welcome').hidden = !!id; el('session-title').textContent = title; el('session-cwd').textContent = cwd || '选择工作目录，开始对话';
    // 发送进行中不覆盖输入框：那条消息还没发出去，切换会话后
    // 用户要能在这里继续重发（U13）。其余情况照常载入目标会话草稿。
    // 附件按会话隔离：上一会话的图片不能留在新会话里被发送出去（U01）。
    this.clearAttachments();
    if (!this.sending) el<HTMLTextAreaElement>('prompt').value = readDraft(this.draftKey());
    document.body.dataset.sessionId = id; this.setRun('idle'); this.notice(''); closeMobileSidebar();
    if (push) history.pushState(null, '', id ? `/?session=${encodeURIComponent(id)}` : '/');
    this.markSelected(); el('chat-scroll').dataset.resetScroll = 'true';
    if (id) void this.refreshHistory();
    if (this.bridge.connected) void this.reconcile().catch((err) => this.fail(err));
    this.workspace?.setCwd(cwd);
  }
  private async newSession(): Promise<void> {
    const response = await this.request<{ roots: string[] }>('files.roots', undefined, '');
    el('workspace-roots').replaceChildren(...response.roots.map((path) => new Option(path, path)));
    el<HTMLInputElement>('cwd-input').value = this.cwd || response.roots[0] || '';
    openDialog('new-dialog');
  }
  private async send(): Promise<void> {
    const input = el<HTMLTextAreaElement>('prompt'); const message = input.value.trim();
    if (!message || this.sending) return;
    this.sending = true; this.updateControls(); const scope = this.scope.current$(); const draftKey = this.draftKey();
    // 排队意图必须在 ensureWorker 之前取：它会刷新会话状态，
    // 而刷新会把单选按钮重置成 Pi 的当前值，晚一步读就丢了用户的选择。
    const busy = this.run !== 'idle';
    const queuedKind = busy ? this.queueKind() : undefined;
    try {
      await this.ensureWorker(scope);
      if (!scope.alive()) {
        // 会话已切换：不能把消息投到新会话（U13），也不静默丢弃。
        // 抛出而不是 return，让 catch 统一给出可见提示；输入与附件未清空。
        throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
      }
      if (!busy) {
        const choice = el<HTMLSelectElement>('model-select').selectedOptions[0];
        if (choice?.dataset.provider && choice.dataset.modelId) await this.request('session.set_model', { provider: choice.dataset.provider, modelId: choice.dataset.modelId }, scope.sessionId);
        this.live.begin(message); this.setRun('running'); el('welcome').hidden = true;
      }
      const images = this.attachments;
      // 目标会话固定为发起时那一个：等待期间切换也不改投（U13）。
      if (busy && queuedKind) await this.sendQueued(message, queuedKind, images, scope);
      else await this.request('session.prompt', { text: message, ...(images.length ? { images: toWire(images) } : {}) }, scope.sessionId);
      if (!scope.alive()) return;
      if (input.value.trim() === message) input.value = '';
      saveDraft(draftKey, ''); this.saveCurrentDraft();
      this.clearAttachments();
      if (busy) this.notify('消息已排队');
      void this.refreshThinking().catch((err) => this.fail(err));
    } catch (error) { this.fail(error); if (this.bridge.connected) void this.reconcile().catch(() => {}); }
    finally { this.sending = false; this.updateControls(); }
  }
  private onMessage(message: Message): void {
    if (message.kind === 'response') return;
    if (message.event === 'terminal.output' || message.event === 'bridge.terminal_closed') { this.workspace?.event(message); return; }
    if (message.sessionId !== this.sessionId) return;
    if (message.kind === 'control') {
      if (message.event === 'bridge.subscription_closed') {
        this.subscribed = '';
        if (this.run !== 'idle') this.notice('实时订阅已结束，正在核对任务状态。');
        if (this.bridge.connected) void this.reconcile().catch((err) => this.fail(err));
      }
      return;
    }
    const event = message as EventMessage;
    if (!this.cursor.accept(event.epoch, event.seq)) return;
    if (event.event === 'worker.exited') { this.setRun('idle'); return; }
    if (event.event !== 'pi.event') return;
    const data = record(event.data);
    this.setRun(runStateAfter(this.run, data));
    if (data.type === 'agent_start' && el('live').hidden) this.live.begin();
    this.live.event(data);
    if (data.type === 'agent_settled') void this.settled().catch((err) => this.fail(err));
    if (data.type === 'extension_ui_request') this.extension(data);
  }
  private async settled(): Promise<void> {
    this.setRun('idle'); this.live.finish(); this.diskSession = true;
    const id = this.sessionId;
    await this.refreshHistory();
    if (id !== this.sessionId) return;
    if (el('turns').querySelector('[data-turn-id]')) this.live.clear();
    this.refreshSessions();
    const stats = record(await this.request('session.stats').catch(() => ({})));
    if (id !== this.sessionId) return;
    el('usage').textContent = [typeof stats.totalMessages === 'number' ? `${stats.totalMessages} 条消息` : '', typeof stats.cost === 'number' ? `$${stats.cost.toFixed(4)}` : ''].filter(Boolean).join(' · ');
  }
  // gotoLeaf 查看指定分支。leafId 为空表示回到磁盘上可恢复的当前分支。
  // 这只是查看，不改 Pi 的状态；要真正确认一个分支仍然走「从此处分支」。
  private gotoLeaf(leafId: string): void {
    if (!this.sessionId || !this.diskSession) return;
    const query = leafId ? `?leafId=${encodeURIComponent(leafId)}` : '';
    void window.htmx.ajax('get', `/ui/sessions/${encodeURIComponent(this.sessionId)}/history${query}`, { target: '#turns', swap: 'innerHTML' })
      .then(() => { if (leafId) this.notify(`已切换到分支 ${leafId.slice(0, 8)} 的视图`); })
      .catch((error) => this.fail(error));
  }
  // forkFrom 是「从此处分支」的唯一实现：回合上的按钮与分支导航面板
  // 都走这里。曾经两处各写一遍，后写的那份还少了对话框关闭，
  // 于是从面板 fork 成功后弹窗不消失。
  private async forkFrom(entryId: string): Promise<void> {
    if (!entryId) { this.notify('缺少要分支的条目 ID', 'warning'); return; }
    try {
      const result = record(await this.command('session.fork', { entryId }));
      const id = text(result.sessionId);
      if (!id) { this.notify('Pi 没有返回新会话 ID', 'warning'); return; }
      this.selectSession(id, this.cwd, '分支会话');
      closeDialog('branch-dialog');
    } catch (error) { this.fail(error); }
  }
  private async refreshHistory(): Promise<void> {
    if (!this.sessionId || !this.diskSession || this.historyLoading === this.sessionId) return;
    const id = this.sessionId; this.historyLoading = id;
    try { await window.htmx.ajax('get', `/ui/sessions/${encodeURIComponent(id)}/history`, { target: '#turns', swap: 'innerHTML' }); }
    finally { if (this.historyLoading === id) this.historyLoading = ''; }
  }
  private refreshSessions(): void { window.htmx.trigger(document.body, 'sessions-refresh'); }
  private markSelected(): void {
    for (const link of document.querySelectorAll<HTMLElement>('[data-session]')) {
      link.classList.toggle('selected', link.dataset.session === this.sessionId);
      if (link.dataset.session === this.sessionId && !this.cwd) { this.cwd = link.dataset.cwd ?? ''; el('session-cwd').textContent = this.cwd; el('session-title').textContent = link.dataset.title ?? '会话'; }
    }
    el('session-count').textContent = String(document.querySelectorAll('[data-session]').length);
  }
  private async search(query: string): Promise<void> {
    if (!query) { this.refreshSessions(); return; }
    try {
      const result = await this.request<unknown>('sessions.search', { query, limit: 50 }, '');
      if (el<HTMLInputElement>('session-search').value.trim() !== query) return;
      const data = record(result); const rows = Array.isArray(result) ? result : (Array.isArray(data.matches) ? data.matches : []);
      const list = el('session-list'); list.replaceChildren();
      for (const value of rows) {
        const item = record(value); const id = text(item.sessionId) || text(item.id); if (!id) continue;
        const link = document.createElement('a'); link.className = 'session-item'; link.href = `/?session=${encodeURIComponent(id)}`; link.dataset.session = id; link.dataset.title = text(item.name) || id; link.textContent = text(item.snippet) || text(item.text) || text(item.name) || id; list.append(link);
      }
      if (!list.children.length) list.textContent = '没有匹配的会话';
    } catch (error) { this.fail(error); }
  }
  private async refreshThinking(selected?: string): Promise<void> {
    if (!this.sessionId) return; const id = this.sessionId;
    const levels = await this.request<string[]>('session.thinking_levels');
    if (id !== this.sessionId) return;
    const select = el<HTMLSelectElement>('thinking-select');
    const current = selected ?? select.value; select.replaceChildren(...levels.map((level) => new Option(level, level)));
    if (levels.includes(current)) select.value = current; select.disabled = this.run !== 'idle' || !levels.length;
  }
  private selectModel(provider: string, id: string, name = id): void {
    this.currentModel = { provider, id, name };
    const select = el<HTMLSelectElement>('model-select');
    let option = Array.from(select.options).find((item) => item.dataset.provider === provider && item.dataset.modelId === id);
    if (!option) { option = new Option(`${name} · ${provider}`, `${provider}/${id}`); option.dataset.provider = provider; option.dataset.modelId = id; select.add(option); }
    select.value = option.value;
  }
  private async changeModel(): Promise<void> {
    if (!this.sessionId) return;
    const option = el<HTMLSelectElement>('model-select').selectedOptions[0];
    if (option?.dataset.provider && option.dataset.modelId) { await this.command('session.set_model', { provider: option.dataset.provider, modelId: option.dataset.modelId }); await this.refreshThinking(); }
  }
  private extension(event: Record<string, unknown>): void {
    const method = text(event.method);
    if (DIALOGS.has(method)) { void this.refreshDialogs().catch((err) => this.fail(err)); return; }
    if (method === 'notify') { this.notify(text(event.message), text(event.notifyType)); return; }
    if (method === 'setTitle') { document.title = text(event.title).slice(0, 200) || 'Pi · 工作台'; return; }
    if (method === 'set_editor_text') { el<HTMLTextAreaElement>('prompt').value = text(event.text).slice(0, 200_000); this.updateControls(); return; }
    if (method === 'setStatus') { const key = text(event.statusKey).slice(0, 128); if (!key) return; if (!event.statusText) this.statuses.delete(key); else if (this.statuses.has(key) || this.statuses.size < 64) this.statuses.set(key, text(event.statusText).slice(0, 2048)); }
    if (method === 'setWidget') {
      const key = text(event.widgetKey).slice(0, 128); if (!key) return;
      if (!Array.isArray(event.widgetLines)) this.widgets.delete(key);
      else if (this.widgets.has(key) || this.widgets.size < 32) this.widgets.set(key, { lines: event.widgetLines.slice(0, 64).map((line) => text(line).slice(0, 1000)), placement: text(event.widgetPlacement) });
    }
    this.renderExtensions();
  }
  private renderExtensions(): void {
    el('ext-status-slot').textContent = Array.from(this.statuses).sort(([a],[b]) => a.localeCompare(b)).map(([,value]) => value).join(' · ');
    el('ext-widgets-before').replaceChildren(); el('ext-widgets-after').replaceChildren();
    for (const [key, widget] of this.widgets) { const node = document.createElement('div'); node.className = 'ext-widget'; node.dataset.widgetKey = key; node.textContent = widget.lines.join('\n'); el(widget.placement === 'belowEditor' ? 'ext-widgets-after' : 'ext-widgets-before').append(node); }
  }
  private async refreshDialogs(): Promise<void> {
    if (!this.sessionId) return;
    this.dialogsDirty = true;
    if (this.dialogsLoading) return;
    this.dialogsLoading = true;
    try {
      while (this.dialogsDirty && this.sessionId) {
        this.dialogsDirty = false;
        const id = this.sessionId; const scope = this.scope.current$();
        const pending = await this.request<{ ids: string[] }>('session.pending_dialogs');
        if (!scope.alive()) continue;
        const shown = Array.from(el('ext-dialog-slot').querySelectorAll<HTMLElement>('[data-dialog-id]')).map((node) => node.dataset.dialogId!).sort();
        const wanted = [...pending.ids].sort();
        if (JSON.stringify(shown) === JSON.stringify(wanted)) continue;
        if (!wanted.length) { el('ext-dialog-slot').replaceChildren(); continue; }
        await window.htmx.ajax('get', `/ui/extensions/dialogs?sessionId=${encodeURIComponent(id)}`, { target: '#ext-dialog-slot', swap: 'innerHTML' });
        scope.write(() => this.openExtensionDialog());
      }
    } finally { this.dialogsLoading = false; }
  }
  private openExtensionDialog(): void {
    const dialog = el('ext-dialog-slot').querySelector<HTMLDialogElement>('dialog');
    if (!dialog || dialog.matches(':modal')) return; if (dialog.open) dialog.close(); dialog.showModal();
    dialog.addEventListener('cancel', (event) => { event.preventDefault(); const form = dialog.querySelector<HTMLFormElement>('form'); if (form) void this.answerDialog(form, null); }, { once: true });
  }
  private async answerDialog(form: HTMLFormElement, button: HTMLButtonElement | null): Promise<void> {
    const dialog = form.closest<HTMLDialogElement>('dialog')!;
    const params: Record<string, unknown> = { id: dialog.dataset.dialogId };
    if (!button || button.name === 'cancelled') params.cancelled = true;
    else if (button.name === 'confirmed') params.confirmed = button.value === 'true';
    else params.value = new FormData(form).get('value') ?? '';
    for (const btn of form.querySelectorAll('button')) btn.disabled = true;
    try { await this.request('session.ui_response', params); dialog.close(); dialog.remove(); await this.refreshDialogs(); await this.reconcile(); }
    catch (error) { this.fail(error); for (const btn of form.querySelectorAll('button')) btn.disabled = false; }
  }
  private showCommands(): void {
    const menu = el('command-menu'); const value = el<HTMLTextAreaElement>('prompt').value;
    menu.replaceChildren(); menu.hidden = !value.startsWith('/') || value.includes(' ') || !this.commands.length;
    if (menu.hidden) return;
    for (const command of this.commands.filter((c) => c.name.startsWith(value.slice(1))).slice(0, 20)) { const button = document.createElement('button'); button.type = 'button'; button.dataset.command = command.name; button.textContent = `/${command.name}  ${command.description}`; menu.append(button); }
  }
  private onClick(event: MouseEvent): void {
    const target = event.target as Element;
    const link = target.closest<HTMLElement>('[data-session]');
    if (link) { event.preventDefault(); this.selectSession(link.dataset.session ?? '', link.dataset.cwd ?? '', link.dataset.title ?? '会话'); return; }
    const command = target.closest<HTMLElement>('[data-command]');
    if (command) { el<HTMLTextAreaElement>('prompt').value = `/${command.dataset.command} `; el('command-menu').hidden = true; el('prompt').focus(); return; }
    const button = target.closest<HTMLElement>('[data-action]');
    if (button) void this.action(button.dataset.action ?? '', button).catch((error) => this.fail(error));
  }
  private async action(action: string, button: HTMLElement): Promise<void> {
    switch (action) {
      case 'new': await this.newSession(); break;
      case 'latest': this.bottom(); break;
      case 'refresh-sessions': this.refreshSessions(); break;
      case 'models-refresh': window.htmx.trigger(document.body, 'models-refresh'); break;
      case 'settings': openDialog('settings-dialog'); window.htmx.trigger(document.body, 'packages-refresh'); break;
      case 'branch': {
        if (!this.branch) {
          const { BranchNavigator } = await import('./branch');
          this.branch = new BranchNavigator(this.bridge, (err) => this.fail(err), (leafId) => this.gotoLeaf(leafId), (entryId) => void this.forkFrom(entryId), () => this.sessionId);
        }
        openDialog('branch-dialog');
        await this.branch.open();
        break;
      }
      case 'branch-refresh': await this.branch?.refresh(); break;
      case 'branch-current': this.gotoLeaf(''); break;
      case 'models-edit': {
        if (!this.models) { const { ModelsEditor } = await import('./models'); this.models = new ModelsEditor(this.bridge, (err) => this.fail(err)); }
        openDialog('models-dialog');
        await this.models.open();
        break;
      }
      case 'models-reload': await this.models?.reload(); break;
      case 'models-save': await this.models?.save(); break;
      case 'models-discover': await this.models?.discover(); break;
      case 'models-test': await this.models?.test(); break;
      case 'session-menu': {
        el<HTMLInputElement>('session-name').value = el('session-title').textContent ?? '';
        openDialog('session-dialog');
        if (this.sessionId) await this.refreshState();
        break;
      }
      case 'abort': await this.request('session.abort'); this.notice('已请求中止，等待 Pi 完成清理。'); break;
      case 'rename': await this.command('session.set_name', { name: el<HTMLInputElement>('session-name').value }); el('session-title').textContent = el<HTMLInputElement>('session-name').value; this.refreshSessions(); break;
      case 'compact': this.setRun('compacting'); try { await this.command('session.compact'); await this.refreshHistory(); } finally { await this.reconcile(); } break;
      case 'clone': { const result = await this.command<{sessionId:string}>('session.clone'); this.selectSession(result.sessionId, this.cwd, '克隆会话'); this.refreshSessions(); break; }
      case 'fork': await this.forkFrom(button.dataset.entryId ?? ''); break;
      case 'stop': await this.request('session.stop', { force: false }); this.setRun('idle'); this.notice('工作进程已释放，历史保留在磁盘。'); break;
      case 'delete': if (this.sessionId && confirm('删除此会话的磁盘记录？此操作无法撤销。')) { await this.request('sessions.delete', { sessionId: this.sessionId }, ''); this.selectSession('', this.cwd, '新会话'); this.refreshSessions(); closeDialog('session-dialog'); } break;
      case 'copy-turn': await navigator.clipboard.writeText(button.closest('.turn')?.querySelector('.turn-assistant .bubble')?.textContent ?? ''); this.notify('已复制'); break;
      case 'commands': this.commands = (await this.command<Record<string,unknown>[]>('session.commands')).map((c) => ({name: text(c.name), description: text(c.description)})); el<HTMLTextAreaElement>('prompt').value = '/'; this.showCommands(); el('prompt').focus(); break;
      case 'export': {
        // 用完整会话 ID，截断只会得到 "history-" 这种没有辨识度的名字。
        const name = `session-${this.sessionId.slice(0, 64)}.html`;
        const result = await this.command<{ path: string }>('session.export_html', { fileName: name });
        const file = text(result.path).split('/').pop() || name;
        this.notify('已导出，开始下载。');
        // 走普通导航而不是 fetch：需要浏览器弹出下载，且要带登录 Cookie。
        location.assign(`/ui/exports/${encodeURIComponent(file)}`);
        break;
      }
      case 'abort-retry': await this.command('session.abort_retry'); this.notify('已请求中止重试。'); await this.reconcile(); break;
      case 'attach': el<HTMLInputElement>('attach-input').click(); break;
      case 'workspace': {
        const panel = el('workspace-panel'); panel.hidden = !panel.hidden; el('workbench').dataset.rightPanel = panel.hidden ? 'closed' : 'open';
        button.setAttribute('aria-expanded', String(!panel.hidden));
        if (!panel.hidden) { if (!this.workspace) { const { Workspace } = await import('./workspace'); this.workspace = new Workspace(this.bridge, (err) => this.fail(err)); } this.workspace.setCwd(this.cwd); await this.workspace.open(); } break;
      }
    }
  }
  // refreshQueueState 反映 Pi 可读回的排队模式与自动压缩。
  // 自动重试没有读回字段，只在用户本次操作时更新，不清空。
  private refreshQueueState(state: State): void {
    const steering = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="steering"]');
    const followUp = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]');
    if (steering && followUp) {
      const kind = state.steeringMode === 'one-at-a-time' ? 'followUp' : 'steering';
      (kind === 'steering' ? steering : followUp).checked = true;
    }
    const compaction = el<HTMLInputElement>('auto-compaction');
    if (state.autoCompactionEnabled !== undefined) compaction.checked = state.autoCompactionEnabled;
    this.updateControls();
  }
  // wireAttachments 接管文件选择、粘贴与拖拽三条入口。
  // 拖拽用计数器而不是 dragenter/dragleave 配对：子元素穿越会误触发 leave。
  private wireAttachments(): void {
    const signal = this.abort.signal;
    const picker = el<HTMLInputElement>('attach-input');
    picker.addEventListener('change', () => { if (picker.files) void this.attach(picker.files); picker.value = ''; }, { signal });
    el<HTMLTextAreaElement>('prompt').addEventListener('paste', (event) => {
      const files = Array.from(event.clipboardData?.files ?? []);
      if (!files.length) return;
      event.preventDefault();
      void this.attach(files);
    }, { signal });
    let depth = 0;
    const composer = el('composer');
    const dropHint = el('composer-drop');
    composer.addEventListener('dragenter', (event) => { event.preventDefault(); depth++; composer.classList.add('dragging'); dropHint.hidden = false; }, { signal });
    composer.addEventListener('dragover', (event) => event.preventDefault(), { signal });
    composer.addEventListener('dragleave', () => { if (--depth <= 0) { depth = 0; composer.classList.remove('dragging'); dropHint.hidden = true; } }, { signal });
    composer.addEventListener('drop', (event) => {
      event.preventDefault(); depth = 0; composer.classList.remove('dragging'); dropHint.hidden = true;
      if (event.dataTransfer?.files.length) void this.attach(event.dataTransfer.files);
    }, { signal });
  }

  // wireLazy 接线惰性内容占位符。事件委托只绑一次，
  // 历史整块替换后无需重新绑定；模块加载失败不影响其余功能。
  private async wireLazy(): Promise<void> {
    try {
      const { wireLazy } = await import('./lazy');
      wireLazy(() => this.sessionId);
    } catch (error) { console.warn('惰性内容接线失败', error); }
  }

  // wireMention 动态加载 @ 补全。它只在用户真的打 @ 时才有用，
  // 不值得进首屏包；加载失败也不该影响其余功能。
  private async wireMention(): Promise<void> {
    try {
      const { FileCompleter } = await import('./mention');
      this.mention = new FileCompleter(el<HTMLTextAreaElement>('prompt'), document.getElementById('mention-menu'), () => this.cwd,
        // files.index 无 query 时返回 {files}，有 query 时返回 {matches}；
        // @ 后刚打完还没有查询词，走的是前者，这里要兜住。
        (cwd, query) => this.request<{ files?: string[]; matches?: { path: string }[] }>('files.index', { path: cwd, query })
          .then((r) => (r.matches ?? (r.files ?? []).map((path) => ({ path })))));
    } catch (error) { console.warn('@ 补全加载失败', error); }
  }

  /**
   * attach 串行处理附件批次。
   *
   * 必须串行：addFiles 的额度判断读的是「调用时」的数组，两个并发批次
   * 各自看到旧的空数组，随后合并成 16 张，越过 8 张上限（U20）。
   * 队列让每一批都在上一批落地之后才判断额度。
   */
  private async attach(files: FileList | File[]): Promise<void> {
    const previous = this.attachQueue;
    // 无论上一批成功与否都要释放队列，否则一次失败会永久堵住后续附件。
    const run = previous.then(() => this.attachBatch(files)).catch((error) => { this.fail(error); });
    this.attachQueue = run.then(() => undefined, () => undefined);
    return run;
  }

  private async attachBatch(files: FileList | File[]): Promise<void> {
    const { added, rejected } = await addFiles(files, this.attachments);
    if (rejected.length) this.notify(rejected.join('；'), 'warning');
    if (!added.length) return;
    this.attachments = this.attachments.concat(added);
    this.renderAttachments();
  }

  private renderAttachments(): void {
    const box = el('attachments');
    box.replaceChildren();
    box.hidden = this.attachments.length === 0;
    this.attachments.forEach((item, index) => {
      const cell = document.createElement('div');
      cell.className = 'attachment';
      const image = document.createElement('img');
      image.src = `data:${item.mimeType};base64,${item.data}`;
      image.alt = item.name;
      const remove = document.createElement('button');
      remove.type = 'button'; remove.textContent = '×'; remove.setAttribute('aria-label', `移除 ${item.name}`);
      remove.addEventListener('click', () => { this.attachments.splice(index, 1); this.renderAttachments(); });
      const meta = document.createElement('span');
      meta.className = 'attachment-meta'; meta.textContent = formatSize(item.size);
      cell.append(image, remove, meta);
      box.append(cell);
    });
  }

  private clearAttachments(): void { this.attachments = []; this.renderAttachments(); }
  private draftKey(): string { return this.sessionId || `new:${this.cwd}`; }
  private saveCurrentDraft(): void { saveDraft(this.draftKey(), el<HTMLTextAreaElement>('prompt').value); }
  private setConnection(online: boolean): void { el('conn-state').textContent = online ? '已连接' : '未连接'; el('conn-state').className = `state state-${online ? 'online' : 'offline'}`; if (online) this.notice(''); this.updateControls(); }
  private setRun(state: RunState): void { this.run = state; el('session-state').textContent = {idle:'就绪',running:'运行中',retrying:'重试中',compacting:'压缩中',waiting_input:'等待确认'}[state]; this.updateControls(); }
  private updateControls(): void { el<HTMLButtonElement>('send-button').disabled = !this.bridge.connected || this.sending || !el<HTMLTextAreaElement>('prompt').value.trim(); el('abort-button').hidden = this.run === 'idle'; const hint = el('queue-hint'); const busy = this.run !== 'idle'; hint.hidden = !busy; if (busy) hint.textContent = this.queueKind() === 'steering' ? '本轮结束后插入指令' : '排到队列末尾，本轮完成后追加'; el<HTMLSelectElement>('model-select').disabled = busy; el<HTMLSelectElement>('thinking-select').disabled = busy || !this.sessionId; }
  // queueKind 读会话对话框里的 radio；缺省 steering，与 Pi 的默认一致。
  // 取值必须与桥的协议一致：steering/followUp，不是 steer。
  private queueKind(): 'steering' | 'followUp' { return document.querySelector<HTMLInputElement>('input[name="queue-kind"]:checked')?.value === 'followUp' ? 'followUp' : 'steering'; }
  // sendQueued 在运行中发送：先把模式同步给桥，再带 streamingBehavior 提交。
  // 不先同步的话，用户改了 radio 但桥仍是旧模式，行为与界面显示不一致。
  private async sendQueued(text: string, kind: 'steering' | 'followUp', images: Attachment[], scope: Scope): Promise<void> {
    // 排队模式与消息都必须发往发起时那个会话，不能跟着 this.sessionId 漂移。
    await this.request('session.set_queue_mode', { kind, mode: kind === 'steering' ? 'all' : 'one-at-a-time' }, scope.sessionId);
    await this.request('session.prompt', { text, streamingBehavior: kind, ...(images.length ? { images: toWire(images) } : {}) }, scope.sessionId);
  }
  private notice(message: string): void { el('connection-notice').textContent = message; el('connection-notice').hidden = !message; }
  private notify(message: string, kind = 'info'): void { void import('./toast').then(({showToast}) => showToast(message, kind === 'error' ? 'error' : kind === 'warning' ? 'warning' : 'info')); }
  private fail(error: unknown): void { const message = error instanceof Error ? error.message : '操作失败'; this.notify(message, 'error'); this.notice(message); }
  dispose(): void { this.saveCurrentDraft(); this.abort.abort(); clearInterval(this.poll); clearTimeout(this.searchTimer); this.live.dispose(); this.workspace?.dispose(); this.bridge.dispose(); }
}
