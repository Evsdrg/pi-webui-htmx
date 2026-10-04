import { BridgeClient, BridgeError } from './bridge';
import { LiveView, EventCursor, record, text, runStateAfter, hasVisibleText, type RunState } from './stream';
import { closeMobileSidebar, readDraft, saveDraft } from './layout';
import { addFiles, toWire, formatSize, WIRE_BUDGET } from './attachments';
import type { Attachment } from './attachments';
import type { Capabilities, EventMessage, Message, Method, WorkerInfo } from '@/types/protocol';
import type { TopbarHost } from './topbar';
import { closeDialog, el, openDialog } from './dom';
import { mountFragmentRequests } from './fragment-requests';
import { SessionScope } from './scope';
import { absoluteUrl, relativeSocketUrl } from '../lib/url';
import type { Scope } from './scope';

const DIALOGS = new Set(['select','confirm','input','editor']);
const ABORT_PENDING_NOTICE = '已请求中止，等待 Pi 完成清理。';
const SUBSCRIPTION_CHECK_NOTICE = '实时订阅已结束，正在核对任务状态。';
// RESYNC_NOTICE：实时流不再可信、正从持久历史重建时的统一提示。
const RESYNC_NOTICE = '实时事件已超出补发窗口，已重新读取历史；生成中的缺失内容将在本轮完成后同步。';

// 长任务命令的等待上限，与桥 protocol/methods.go 的 Spec.Timeout 对应。
// 桥侧给的时间更长，前端先放弃只会让用户以为失败（B66）。
const COMMAND_TIMEOUTS: Partial<Record<Method, number>> = {
  'session.compact': 300_000,
  'session.bash': 300_000,
  'session.export_html': 120_000,
};

function commandTimeout(method: Method): number {
  return COMMAND_TIMEOUTS[method] ?? 30_000;
}
interface State { sessionId: string; sessionName?: string; isStreaming: boolean; isCompacting: boolean; thinkingLevel?: string; model?: { id: string; provider: string; name: string } | null; pendingMessageCount?: number; steeringMode?: string; followUpMode?: string; autoCompactionEnabled?: boolean }
type ModelChoice = { provider: string; id: string; name: string };

function knownModel(model: State['model']): model is ModelChoice {
  return !!model?.provider && !!model.id && model.provider.toLowerCase() !== 'unknown' && model.id.toLowerCase() !== 'unknown';
}

export class Workbench {
  // WS 端点用**相对路径**解析：本地形态下是 `{host}/api/v1/ws`，
  // 经 relay 的设备前缀形态下是 `{host}/d/{id}/api/v1/ws`——
  // 同一份产物在两种部署下都指向正确的前缀（B54）。
  readonly bridge = new BridgeClient(relativeSocketUrl('api/v1/ws'));
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
  /**
   * pendingHistory 登记最近一次历史请求的归属。
   * beforeSwap 用它判断「这个响应是否还属于当前会话与当前代次」，
   * 只看 URL 无法覆盖 A→B→A（U17/U18）。
   */
  private pendingHistory: { sessionId: string; epoch: number } = { sessionId: '', epoch: 0 };
  private searchFocus: { sessionId: string; entryId: string; epoch: number } | undefined;
  /**
   * autoRetryBySession 按会话记录自动重试偏好。
   * Pi 的 RPC 没有 auto-retry 读回字段，桥也无从得知；因此这是本地偏好，
   * 不是 Pi 的实时状态——界面必须这样说，不能让未勾选看起来像「已确认关闭」。
   */
  private autoRetryBySession = new Map<string, boolean>();
  /**
   * thinkingBySession 按会话记住思考强度选择；空字符串表示「自动」——
   * 不向 Pi 发送 set_thinking，由 Pi 自己按 settings 与模型能力决定。
   */
  private thinkingBySession = new Map<string, string>();
  /** toolPresetBySession 记住每个会话的工具预设，跨启动沿用。 */
  private toolPresetBySession = new Map<string, string>();
  private queueChoiceBySession = new Map<string, 'steering' | 'followUp'>();
  private statuses = new Map<string, string>();
  private widgets = new Map<string, { lines: string[]; placement: string }>();
  private commands: { name: string; description: string }[] = [];
  private poll: ReturnType<typeof setInterval> | undefined;
  private searchTimer: ReturnType<typeof setTimeout> | undefined;
  private historyLoading = '';
  private historyRefreshPending = false;
  private subscribed = '';
  private dialogsLoading = false;
  private dialogsDirty = false;
  private cancelSessionFragments = () => {};
  private dirRequest: XMLHttpRequest | undefined;
  private workspace: import('./workspace').Workspace | undefined;
  private models: import('./models').ModelsEditor | undefined;
  private branch: import('./branch').BranchNavigator | undefined;
  private mention: import('./mention').FileCompleter | undefined;
  private currentModel: ModelChoice | undefined;
  private historicalModel: { provider: string; id: string } | undefined;
  private modelIntent: { provider: string; id: string } | undefined;
  private modelUnavailable = false;
  /** sessionTitle 是当前会话名；侧栏列表负责显示，这里只保存供重命名等流程使用。 */
  private sessionTitle = '';
  /** searchQuery 记录当前搜索词，用于在片段落地后判断结果是否已过期。 */
  private searchQuery = '';
  /** contextWindow 记录当前模型的上下文窗口，供「系统」面板显示。 */
  private contextWindow = 0;

  constructor(private readonly bottom: () => void) {}
  start(): void {
    // 文件浏览器已常驻侧栏下半，Workspace 不能再等右面板第一次打开才装载——
    // 那之前侧栏里的目录点击没有任何监听。仍是动态 import：它拉进 highlight、
    // ansi、terminal 等只在用到时才需要的依赖，首屏预算不受影响。
    void import('./workspace').then(({ Workspace }) => {
      if (this.abort.signal.aborted) return;
      this.workspace = new Workspace(this.bridge, (err) => this.fail(err));
      if (this.cwd) this.workspace.setCwd(this.cwd);
    }).catch((err) => this.fail(err));
    const signal = this.abort.signal;
    this.cancelSessionFragments = mountFragmentRequests(() => this.scope.epoch, signal);
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
    // 目录列表每次换页后回显路径；用 htmx 自己的事件而不是轮询。
    document.body.addEventListener('htmx:afterSwap', (event) => {
      if ((event.target as HTMLElement).id === 'dir-list') this.syncDirInput();
    }, { signal });
    // 目录选择器是 shell 的一部分，但精简的测试夹具可能不渲染它；
    // 缺元素时静默跳过，不让整个工作台起不来。
    document.addEventListener('htmx:beforeRequest', (event) => {
      const detail = (event as CustomEvent).detail;
      if (detail?.target?.id === 'dir-list') { this.dirRequest = detail.xhr; this.updateDirUse(); }
    }, { signal });
    document.addEventListener('htmx:afterRequest', (event) => {
      if ((event as CustomEvent).detail?.xhr === this.dirRequest) { this.dirRequest = undefined; this.updateDirUse(); }
    }, { signal });
    const dirInput = document.getElementById('cwd-input') as HTMLInputElement | null;
    dirInput?.addEventListener('input', () => {
      const list = document.getElementById('dir-list');
      if (list) list.dataset.requestScope = String(Number(list.dataset.requestScope || '0') + 1);
      this.updateDirUse();
    }, { signal });
    const dirGo = document.getElementById('dir-go');
    dirGo?.addEventListener('click', () => {
      const path = el<HTMLInputElement>('cwd-input').value.trim();
      if (!path) return;
      const requested = document.getElementById('dir-request') as HTMLInputElement | null;
      if (requested) requested.value = path;
      const use = document.getElementById('dir-use') as HTMLButtonElement | null;
      if (use) use.disabled = true;
      window.htmx.trigger(document.body, 'dirs-refresh');
    }, { signal });
    // 两个配置面板共用一套「左栏导航 → 右栏内容」的切换逻辑，
    // 差别只在 data 属性前缀。写成一个函数避免两处漂移。
    const wireConfigNav = (navAttr: string, panelAttr: string, onActivate?: (section: string) => void) => {
      document.addEventListener('click', (event) => {
        const item = (event.target as Element).closest<HTMLElement>(`[${navAttr}]`);
        if (!item) return;
        const section = item.dataset[navAttr === 'data-settings-section' ? 'settingsSection' : 'modelsSection'] ?? '';
        if (!section) return;
        for (const other of document.querySelectorAll<HTMLElement>(`[${navAttr}]`)) {
          if (other === item) other.setAttribute('aria-current', 'page'); else other.removeAttribute('aria-current');
        }
        for (const panel of document.querySelectorAll<HTMLElement>(`[${panelAttr}]`)) panel.hidden = panel.dataset[panelAttr === 'data-settings-panel' ? 'settingsPanel' : 'modelsPanel'] !== section;
        onActivate?.(section);
      }, { signal });
    };
    // 扩展分节的内容按需拉取：只在第一次切到它时请求。
    // 之前只在打开设置为 general 时触发过一次，点「扩展」永远停在占位文字上。
    let packagesLoaded = false;
    document.addEventListener('htmx:afterSwap', (event) => {
      const detail = (event as CustomEvent).detail;
      if (detail?.target?.id === 'packages-slot') packagesLoaded = detail.xhr?.status === 200 && !!detail.target.querySelector('[data-packages-loaded]');
    }, { signal });
    wireConfigNav('data-settings-section', 'data-settings-panel', (section) => {
      if (section !== 'extensions' || packagesLoaded) return;
      window.htmx.trigger(document.body, 'packages-refresh');
    });
    // 模型配置的树点击先问编辑器：命中树节点就由它切面板，
    // 没命中才按通用导航处理（「JSON 源码」那一项）。
    document.addEventListener('click', (event) => {
      const models = document.getElementById('models-dialog') as HTMLDialogElement | null;
      if (!models?.open) return;
      const target = (event.target as Element).closest<HTMLElement>('#models-tree [data-models-provider],#models-tree [data-models-add-model],#models-tree [data-models-add-provider]');
      if (target && this.models?.handleClick(target)) event.preventDefault();
    }, { signal });
    document.addEventListener('click', (event) => {
      const item = (event.target as Element).closest<HTMLElement>('[data-models-section]');
      if (item?.dataset.modelsSection === 'json') this.models?.select('json');
    }, { signal });
    el('new-form').addEventListener('submit', (event) => {
      event.preventDefault(); const cwd = el<HTMLInputElement>('cwd-input').value.trim();
      if (!cwd || !this.directoryReady()) return;
      const firstDirectory = !this.sessionId && !this.cwd;
      const input = el<HTMLTextAreaElement>('prompt');
      const draft = firstDirectory ? input.value : '';
      const model = firstDirectory ? this.selectedModel() : undefined;
      const queueChoice = firstDirectory ? this.queueChoiceBySession.get(this.queueChoiceKey()) : undefined;
      this.selectSession('', cwd, '新会话');
      if (firstDirectory) {
        input.value = draft;
        if (model) { this.modelIntent = model; this.renderModel(); }
        if (queueChoice) {
          this.queueChoiceBySession.set(this.queueChoiceKey(), queueChoice);
          const radio = document.querySelector<HTMLInputElement>(`input[name="queue-kind"][value="${queueChoice}"]`);
          if (radio) radio.checked = true;
        }
        this.saveCurrentDraft(); saveDraft('new:', ''); this.updateControls();
      }
      closeDialog('new-dialog'); input.focus();
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
      // 偏好按会话记忆：Pi 不提供 auto-retry 的读回字段，
      // 跨会话共用一个 DOM 状态会把上一会话的选择带到新会话（U14）。
      this.autoRetryBySession.set(this.scope.current || '', enabled);
      void this.command('session.set_auto_retry', { enabled }).then(() => this.notify(enabled ? '已开启自动重试。' : '已关闭自动重试。')).catch((err) => { this.fail(err); el<HTMLInputElement>('auto-retry').checked = !enabled; });
    }, { signal });
    el('thinking-select').addEventListener('change', () => {
      const level = el<HTMLSelectElement>('thinking-select').value;
      this.thinkingBySession.set(this.sessionId, level);
      // 「自动」= 不覆盖：不发命令，只记住本地偏好。Pi 没有对应的读回字段，
      // 因此不能用一次假 set 把状态「确认」下来。
      if (!level) { this.notify('思考强度已设为自动：由模型与 Pi 配置决定。'); return; }
      void this.command('session.set_thinking', { level }).catch((err) => this.fail(err));
    }, { signal });
    // 两个预设入口共用同一处理：先把快捷选择的值同步到面板，再走切换。
    // 工具预设只有输入栏一个选择器（面板里不再重复一份）。
    el('tool-preset-quick').addEventListener('change', () => {
      void this.topbar().then((m) => m.changeToolPreset(this.host)).catch((err) => this.fail(err));
    }, { signal });
    el('title-form').addEventListener('submit', (event) => { event.preventDefault(); void this.topbar().then((m) => m.saveLocalTitle(this.host)).catch((err) => this.fail(err)); }, { signal });
    for (const action of ['panel-info', 'panel-title', 'panel-system', 'panel-tools', 'panel-mc']) {
      document.querySelector(`[data-action="${action}"]`)?.addEventListener('click', () => this.toggleTopPanel(action), { signal });
      document.querySelector(`[data-action="${action}-close"]`)?.addEventListener('click', () => this.toggleTopPanel(action, false), { signal });
    }
    // 「完整历史」与 Pi Web 同义：新标签页阅读导出的 HTML。
    document.querySelector('[data-action="full-history"]')?.addEventListener('click', () => {
      void this.topbar().then((m) => m.openFullHistory(this.host)).catch((err) => this.fail(err));
    }, { signal });
    for (const option of document.querySelectorAll<HTMLInputElement>('input[name="queue-kind"]')) {
      option.addEventListener('change', () => {
        if (!option.checked) return;
        this.queueChoiceBySession.set(this.queueChoiceKey(), option.value === 'followUp' ? 'followUp' : 'steering');
        this.updateControls();
      }, { signal });
    }
    // 记忆分区、筛选与分页动作由 mc.html 声明。
    document.addEventListener('click', (event) => this.onClick(event), { signal });
    document.addEventListener('keydown', (event) => { if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); void this.newSession().catch((err) => this.fail(err)); } }, { signal });
    // Escape 关闭已打开的顶栏面板（同时只开一个）。
    document.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape') return;
      for (const action of ['panel-info', 'panel-title', 'panel-system', 'panel-tools', 'panel-mc']) {
        if (!el(action === 'panel-info' ? 'panel-info' : action).hidden) { this.toggleTopPanel(action, false); return; }
      }
    }, { signal });
    window.addEventListener('popstate', () => this.selectSession(new URL(location.href).searchParams.get('session') ?? '', '', '会话', false), { signal });
    document.addEventListener('visibilitychange', () => { if (!document.hidden && this.bridge.connected) void this.reconcile().catch((err) => this.fail(err)); }, { signal });
    document.addEventListener('htmx:beforeSwap', (event) => {
      const detail = (event as CustomEvent).detail as { target?: HTMLElement; xhr?: XMLHttpRequest; shouldSwap: boolean };
      const url = detail.xhr?.responseURL;
      if (!url) return;
      const response = new URL(url);
      // 按会话 ID 拒绝只是第一层：A→B→A 时两个代次共用同一个 URL，
      // 单看 URL 会放行旧代次的响应（U17/U18）。代次由 scope 记录，
      // 请求发起时一并登记，这里同时校验两者。
      if (detail.target?.id === 'turns') {
        const wanted = this.pendingHistory;
        const sameSession = response.pathname === `/ui/sessions/${encodeURIComponent(wanted.sessionId)}/history`;
        const sameEpoch = wanted.epoch === this.scope.epoch && wanted.sessionId === this.scope.current;
        if (!sameSession || !sameEpoch) detail.shouldSwap = false;
        else if (detail.xhr?.status === 204 && detail.xhr.getResponseHeader('X-Session-Unsaved') === '1') {
          this.diskSession = false;
          el('unsaved-branch').hidden = false;
          detail.shouldSwap = false;
        }
      }
      if (detail.target?.id === 'ext-dialog-slot' && response.searchParams.get('sessionId') !== this.sessionId) detail.shouldSwap = false;
    }, { signal });
    document.addEventListener('htmx:afterSwap', (event) => {
      const detail = (event as CustomEvent).detail as { target?: HTMLElement; xhr?: XMLHttpRequest };
      const target = detail?.target;
      if (target?.id === 'session-list') { this.markSelected(); this.finishSearch(); }
      if (target?.id === 'ext-dialog-slot') this.openExtensionDialog();
      if (target?.id === 'turns') {
        const url = detail.xhr?.responseURL ? new URL(detail.xhr.responseURL) : undefined;
        if (url && (url.pathname !== `/ui/sessions/${encodeURIComponent(this.sessionId)}/history` || url.searchParams.has('before'))) return;
        this.diskSession = true; el('unsaved-branch').hidden = true;
        el('history-scope').hidden = !url?.searchParams.has('leafId');
        const marker = target.querySelector<HTMLElement>('[data-history-model-provider]');
        if (marker) {
          const provider = marker.dataset.historyModelProvider ?? '';
          const id = marker.dataset.historyModelId ?? '';
          this.historicalModel = provider && id ? { provider, id } : undefined;
          this.renderModel();
        }
        const focus = this.searchFocus;
        if (focus && focus.sessionId === this.sessionId && focus.epoch === this.scope.epoch && url?.searchParams.get('leafId') === focus.entryId) {
          const entry = Array.from(target.querySelectorAll<HTMLElement>('[data-search-entry-id]'))
            .find((node) => node.dataset.searchEntryId === focus.entryId);
          const turn = entry?.closest<HTMLElement>('[data-turn-id]');
          if (turn) {
            turn.classList.add('search-target'); turn.tabIndex = -1;
            turn.focus({ preventScroll: true }); turn.scrollIntoView?.({ block: 'center' });
          }
          this.searchFocus = undefined;
        }
      }
      if (target?.id === 'model-select') this.renderModel();
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
      const response = await fetch('api/v1/capabilities', { cache: 'no-store' });
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
      const response = await fetch('api/v1/auth', { method: 'POST', headers: { Authorization: `Bearer ${input.value}` } });
      if (!response.ok) throw new Error(response.status === 401 ? '令牌无效，请重试。' : `登录失败（${response.status}）`);
      input.value = ''; await this.authenticate();
    } catch (error) { el('auth-error').textContent = error instanceof Error ? error.message : '登录失败'; }
    finally { button.disabled = false; }
  }
  private request<T = unknown>(method: Method, params?: unknown, session = this.sessionId): Promise<T> {
    if (!this.capabilities.has(method)) return Promise.reject(new BridgeError('unsupported_method', `当前桥不支持 ${method}`));
    return this.bridge.request<T>(method, session, params, commandTimeout(method));
  }
  private async ensureWorker(scope: Scope = this.scope.current$()): Promise<Scope> {
    const oldId = this.sessionId;
    if (!oldId && !this.cwd) { await this.newSession(); throw new Error('请先选择工作目录，再发送消息。'); }
    // 工具预设只在拉起进程时生效，因此每次启动都带上该会话上次的选择。
    const preset = this.toolPresetBySession.get(this.queueChoiceKey()) ?? '';
    const info = await this.request<WorkerInfo>('session.start', { cwd: this.cwd, ...(preset ? { toolPreset: preset } : {}) }, oldId);
    if (!scope.alive()) throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
    let activeScope = scope;
    if (!oldId) {
      const queueChoice = this.queueChoiceBySession.get(this.queueChoiceKey());
      // Pi 分配新 ID 是本次发送自身的身份迁移；旧 scope 失效后必须
      // 交给新代次继续执行，不能误判为用户主动切换会话。
      this.scope.switchTo(info.sessionId);
      this.cancelSessionFragments();
      activeScope = this.scope.current$();
      this.sessionId = info.sessionId; this.cwd = info.cwd;
      if (queueChoice) this.queueChoiceBySession.set(info.sessionId, queueChoice);
      if (preset) this.toolPresetBySession.set(info.sessionId, preset);
      document.body.dataset.sessionId = this.sessionId;
      history.replaceState(null, '', `/?session=${encodeURIComponent(info.sessionId)}`);
    }
    this.cwd = info.cwd;
    await this.subscribe();
    await this.refreshState(activeScope);
    // 启动后立刻取一次统计：上下文用量只有 worker 持有模型时才非空，
    // 等到第一轮 settled 才显示会让用户以为没有这个指标。
    void this.refreshUsage(activeScope);
    return activeScope;
  }
  /** refreshUsage 拉取会话统计，填充底栏用量与顶栏上下文占比。 */
  private refreshUsage(scope: Scope): void {
    if (!scope.alive()) return;
    void this.topbar().then((m) => m.renderUsage(this.host, scope.sessionId)).catch(() => {});
  }
  /**
   * command 把目标会话固定为发起时那一个。
   *
   * 以前是 await ensureWorker() 之后再用 this.request 的默认参数读
   * this.sessionId——等待期间切会话，操作就落到了新会话上（U03）。
   * 现在 target 在发起时取定，整条链路都显式传它。
   */
  private async command<T = unknown>(method: Method, params?: unknown, scope: Scope = this.scope.current$()): Promise<T> {
    const activeScope = await this.ensureWorker(scope);
    return this.request<T>(method, params, activeScope.sessionId);
  }
  // refreshState 读取一次会话状态并同步界面。
  // 只按会话作用域守卫：状态读取发生在已处理的事件之后，内容是更新的；
  // 若再用 seq 比较，一流式输出就会让刷新永远放弃，模型下拉停留在占位项。
  private async refreshState(scope: Scope = this.scope.current$()): Promise<void> {
    let state: State;
    try { state = await this.request<State>('session.state'); } catch { return; }
    if (!scope.alive()) return;
    if (state.sessionName) this.sessionTitle = state.sessionName;
    this.applyStateModel(state);
    await this.refreshThinking(state.thinkingLevel);
    this.refreshQueueState(state);
  }
  private async subscribe(): Promise<void> {
    const id = this.sessionId; if (!id || this.subscribed === id) return;
    // 归属在发起时固定：等待确认期间切了会话，迟到的确认不得改写
    // 新会话的游标（U16）——事件帧已由 accept 挡住旧 epoch，
    // 但确认帧此前没有守卫。
    const scope = this.scope.current$();
    const saved = this.cursor.epoch ? { epoch: this.cursor.epoch, afterSeq: this.cursor.seq } : {};
    // 确认帧里的 epoch/seq 是权威起点：它是桥对「这条订阅属于哪个工作进程」
    // 的回答，也是游标唯一允许切换 epoch 的地方（U16）。
    const begin = (ack: { epoch?: string; seq?: number } | undefined): void => {
      if (ack?.epoch && scope.alive()) this.cursor.begin(ack.epoch, ack.seq ?? 0);
    };
    try { begin(await this.request<{ epoch?: string; seq?: number }>('session.subscribe', saved, id)); } catch (error) {
      if (!(error instanceof BridgeError) || error.code !== 'resync_required') throw error;
      if (!scope.alive()) return;
      this.cursor.reset(); this.live.clear();
      this.notice(RESYNC_NOTICE);
      await this.refreshHistory(); begin(await this.request<{ epoch?: string; seq?: number }>('session.subscribe', {}, id));
    }
    if (id === this.sessionId) {
      this.subscribed = id;
      void this.pullExtensionStatus(scope).catch(() => {});
    }
  }
  // pullExtensionStatus 在订阅确认后补齐扩展状态行。
  // 桥把 setStatus 记在 worker 上（B36），所以页面加载、重订阅与
  // 重连后都能拿到已发生的状态行，不必等插件下次 setStatus。
  // 只补本地缺的 key：快照可能比已收到的 WS 增量旧，覆盖会让状态倒退。
  private async pullExtensionStatus(scope: Scope): Promise<void> {
    const data = await this.request<{ epoch?: string; statuses?: Record<string, string> }>('session.ext_status');
    if (!scope.alive()) return;
    for (const [key, value] of Object.entries(record(data.statuses))) {
      const line = text(value).slice(0, 2048);
      if (line && !this.statuses.has(key) && this.statuses.size < 64) this.statuses.set(key, line);
    }
    this.renderExtensions();
  }
  // resyncFromStream 是「实时流不再可信、必须从持久历史重建」的统一入口。
  // 三条路径共用它：桥明确要求重同步（resync_required）、订阅被关闭且
  // 带 resyncRequired、事件因体积被省略（bridge.event_omitted，U15）。
  // 只做只读重建：重读历史、重订、刷新扩展对话；绝不重发副作用命令。
  //
  // 全部重绘副作用（清实时层、换历史、重订）受合并窗口约束，见 runResync：
  // 没有它时有一条真实的恶性循环——订阅被桥摘除（慢消费者，大会话 / 长任务
  // 高频事件下浏览器主线程正在忙）→ 立即重读历史并重订 → 主线程又被渲染
  // 压住 → 再次被摘除。每一次循环都会把 #turns 整体换入一次，用户看到的
  // 是两段对话在旧页与新页之间快速来回切换；刷新页面之所以「没事」，只是
  // 打破了当时那个循环。
  private resyncFromStream(reason: string): void {
    if (!this.sessionId) return;
    // 退订与游标复位是幂等的安全动作，始终执行：窗口合并的是重绘，
    // 不是「流已断」这个事实。
    this.subscribed = ''; this.cursor.reset();
    if (this.resyncDeferred()) return;
    this.live.clear();
    this.notice(reason);
    this.runResync();
    void this.refreshDialogs().catch((err) => this.fail(err));
  }

  // 合并窗口：首个请求立即执行，随后进入冷却窗；窗口内的重复请求只记
  // 一次「待补」，窗口结束时若仍有积压就再执行一轮。效果是重订节奏被
  // 压到冷却窗一级，给主线程留出消费事件的时间，循环自行消散。
  private resyncAt = 0;
  private resyncTimer: ReturnType<typeof setTimeout> | undefined;
  private resyncPending = false;
  private static readonly RESYNC_COOLDOWN_MS = 1200;
  // resyncDeferred 在冷却窗口内时记下积压并返回 true。
  private resyncDeferred(): boolean {
    const now = Date.now();
    if (this.resyncTimer === undefined && now - this.resyncAt >= Workbench.RESYNC_COOLDOWN_MS) return false;
    this.resyncPending = true;
    return true;
  }
  // runResync 执行一轮「重读历史 + 重订阅」并开启新窗口。
  private runResync(): void {
    if (!this.bridge.connected) return;
    this.resyncAt = Date.now();
    void this.refreshHistory().catch((err) => this.fail(err));
    void this.reconcile().catch((err) => this.fail(err));
    clearTimeout(this.resyncTimer);
    this.resyncTimer = setTimeout(() => {
      this.resyncTimer = undefined;
      if (!this.resyncPending) return;
      this.resyncPending = false;
      // 窗口内积压的请求：补一轮（实时层同样可能已不可信）。
      this.live.clear();
      this.runResync();
    }, Workbench.RESYNC_COOLDOWN_MS);
  }
  private async reconcile(): Promise<void> {
    const scope = this.scope.current$();
    const workers = await this.request<WorkerInfo[]>('worker.list', undefined, '');
    if (!scope.alive()) return;
    const worker = workers.find((w) => w.sessionId === this.sessionId);
    if (!worker) {
      this.currentModel = undefined; this.modelUnavailable = false; this.renderModel();
      this.setRun('idle'); return;
    }
    this.cwd = worker.cwd;
    await this.subscribe();
    const stateSeq = this.cursor.seq;
    const state = await this.request<State>('session.state');
    if (!scope.alive() || stateSeq !== this.cursor.seq) return;
    const wasBusy = this.run !== 'idle';
    const busy = state.isStreaming || state.isCompacting || (state.pendingMessageCount ?? 0) > 0;
    this.setRun(busy ? (state.isCompacting ? 'compacting' : 'running') : 'idle');
    if (state.sessionName) this.sessionTitle = state.sessionName;
    this.applyStateModel(state);
    if (wasBusy && !busy) await this.settled();
    else if (!busy && el('connection-notice').textContent === SUBSCRIPTION_CHECK_NOTICE) this.notice('');
    await this.refreshThinking(state.thinkingLevel);
    this.refreshQueueState(state);
    await this.refreshDialogs();
  }
  private selectSession(id: string, cwd: string, title: string, push = true, entryId = '', persisted = true): void {
    // 注意：这里刻意不重置 pendingHistory。它记录的是「最近一次发起的历史
    // 请求」的归属，切换会话后代次已变，迟到的旧响应会被 beforeSwap 拒绝；
    // 若在这里改写成当前值，就识别不出「切换前发起、切换后才到达」的响应。
    this.saveCurrentDraft(); const previous = this.sessionId; this.scope.switchTo(id);
    this.cancelSessionFragments();
    this.searchFocus = entryId ? { sessionId: id, entryId, epoch: this.scope.epoch } : undefined;
    if (previous && this.bridge.connected) void this.request('session.unsubscribe', undefined, previous).catch(() => {});
    this.sessionId = id; this.subscribed = ''; this.cwd = cwd; this.diskSession = !!id && persisted; this.cursor.reset(); this.live.clear();
    const choice = this.queueChoiceBySession.get(this.queueChoiceKey()) ?? 'steering';
    const queueRadio = document.querySelector<HTMLInputElement>(`input[name="queue-kind"][value="${choice}"]`);
    if (queueRadio) queueRadio.checked = true;
    this.currentModel = undefined; this.historicalModel = undefined; this.modelIntent = undefined; this.modelUnavailable = false;
    const modelSelect = el<HTMLSelectElement>('model-select');
    modelSelect.querySelectorAll('option[data-runtime-model]').forEach((option) => option.remove());
    this.renderModel();
    this.statuses.clear(); this.widgets.clear(); this.renderExtensions(); this.commands = [];
    el('turns').replaceChildren(); el('older-slot').replaceChildren(); el('ext-dialog-slot').replaceChildren();
    el('history-scope').hidden = true; el('unsaved-branch').hidden = !id || persisted;
    // 编辑横幅属于上一个会话的叶子位置，切换后必须清掉。
    el('edit-scope').hidden = true;
    el('welcome').hidden = !!id; this.sessionTitle = title; this.cwd = cwd;
    // 发送进行中不覆盖输入框：那条消息还没发出去，切换会话后
    // 用户要能在这里继续重发（U13）。其余情况照常载入目标会话草稿。
    // 附件按会话隔离：上一会话的图片不能留在新会话里被发送出去（U01）。
    this.clearAttachments();
    // 自动重试是本地偏好：切换会话时套用该会话上次的选择，默认关闭。
    el<HTMLInputElement>('auto-retry').checked = this.autoRetryBySession.get(id) ?? false;
    // 思考强度同样按会话记住；没有记录时显示「自动」。
    const rememberedThinking = this.thinkingBySession.get(id);
    const thinkingSelect = el<HTMLSelectElement>('thinking-select');
    thinkingSelect.value = rememberedThinking && Array.from(thinkingSelect.options).some((option) => option.value === rememberedThinking) ? rememberedThinking : '';
    // 工具预设在 worker 启动时生效；这里只回显该会话上次的选择。
    const presetSelect = document.getElementById('tool-preset-quick') as HTMLSelectElement | null;
    if (presetSelect) presetSelect.value = this.toolPresetBySession.get(id) ?? 'default';
    if (!this.sending) el<HTMLTextAreaElement>('prompt').value = readDraft(this.draftKey());
    document.body.dataset.sessionId = id; this.setRun('idle'); this.notice(''); closeMobileSidebar();
    // URL 里的路径部分要保留：设备前缀部署时文档在 `/d/{id}/`，
    // 写死 `/?session=…` 会把地址栏（以及之后的相对解析）拽回根路径（B54）。
    if (push) {
      const path = location.pathname;
      history.pushState(null, '', id ? `${path}?session=${encodeURIComponent(id)}` : path);
    }
    this.markSelected(); el('chat-scroll').dataset.resetScroll = 'true';
    if (id && this.diskSession) void this.refreshHistory(entryId);
    if (this.bridge.connected) void this.reconcile().catch((err) => this.fail(err));
    this.workspace?.setCwd(cwd);
    // 文件树常驻侧栏，但 Workspace 是懒加载的：从没打开过右面板时它还不存在，
    // setCwd 便无从调用，树会停在空路径上（服务端回退到第一个根）。
    // 这里由 workbench 直接把新目录交给片段，不依赖 Workspace 是否已装载。
    const filesPath = document.getElementById('files-path') as HTMLInputElement | null;
    if (filesPath) filesPath.value = cwd;
    window.htmx.trigger(document.body, 'files-refresh');
    this.syncNewSessionButton();
    // 连上之后补一次：页面可能在 ?session= 之外打开，也可能一个会话都没选。
    // 没有 cwd 时列表保持空提示，那是对的——没有项目就没什么可列。
    void this.reconcile().then(() => {
      if (!(document.getElementById('files-path') as HTMLInputElement | null)?.value) {
        void this.bridge.request<{ roots: string[] }>('files.roots', undefined, '').then((data) => {
          const first = data.roots[0] ?? '';
          if (first && !(document.getElementById('files-path') as HTMLInputElement | null)?.value) {
            const input = document.getElementById('files-path') as HTMLInputElement | null;
            if (input) input.value = first;
            window.htmx.trigger(document.body, 'files-refresh');
          }
        }).catch(() => { /* 读不到根时留空提示，不打扰用户 */ });
      }
    }).catch(() => { /* reconcile 自己会报错 */ });
  }
  /** 打开「设置与扩展」并切换到指定分节。模型配置不在这里：它由顶栏与
      * 输入框下方的模型选择器负责，设置面板只管本机外观与已装扩展。 */
  private openSettings(section: string): void {
    const dialog = document.getElementById('settings-dialog') as HTMLDialogElement | null;
    if (!dialog) return;
    for (const item of document.querySelectorAll<HTMLElement>('[data-settings-section]')) {
      const active = item.dataset.settingsSection === section;
      if (active) item.setAttribute('aria-current', 'page'); else item.removeAttribute('aria-current');
    }
    for (const panel of document.querySelectorAll<HTMLElement>('[data-settings-panel]')) {
      panel.hidden = panel.dataset.settingsPanel !== section;
    }
    const sub = document.getElementById('settings-sub');
    if (sub) sub.textContent = section === 'extensions' ? '已安装的包' : '外观与对话';
    openDialog('settings-dialog');
    if (section === 'extensions') window.htmx.trigger(document.body, 'packages-refresh');
  }

  private async newSession(): Promise<void> {
    const response = await this.request<{ roots: string[] }>('files.roots', undefined, '');
    const start = this.cwd || response.roots[0] || '';
    el<HTMLInputElement>('cwd-input').value = start;
    // 路径输入框先于列表刷新填好：列表用 hx-include 读它，
    // 顺序反了就会拿到上一次的路径。
    el<HTMLInputElement>('cwd-input').value = start;
    const current = document.getElementById('dir-current') as HTMLInputElement | null;
    if (current) current.value = '';
    const requested = document.getElementById('dir-request') as HTMLInputElement | null;
    if (requested) requested.value = start;
    this.updateDirUse();
    openDialog('new-dialog');
    // hx-trigger 上的 load 只在元素首次插入 DOM 时触发，第二次打开对话框
    // 不会重新请求，所以这里显式触发一次。
    window.htmx.trigger(document.body, 'dirs-refresh');
    this.syncNewSessionButton();
  }

  /** 目录列表换页后把新路径回显到输入框。当前路径由片段的带外交换写进
      * #dir-current，输入框只是它的可见形态——两者不保持一致的话，
      * 用户改完输入框再刷新会读到旧路径。 */
  private syncDirInput(): void {
    const current = document.getElementById('dir-current') as HTMLInputElement | null;
    const loaded = document.querySelector<HTMLElement>('#dir-list [data-dir-loaded-path]');
    if (current && loaded && current.value === loaded.dataset.dirLoadedPath) el<HTMLInputElement>('cwd-input').value = current.value;
    this.updateDirUse();
  }

  private directoryReady(): boolean {
    const current = document.getElementById('dir-current') as HTMLInputElement | null;
    if (!current) return true; // 精简嵌入页没有目录浏览器。
    const loaded = document.querySelector<HTMLElement>('#dir-list [data-dir-loaded-path]');
    return !this.dirRequest && !!current.value && current.value === loaded?.dataset.dirLoadedPath && current.value === el<HTMLInputElement>('cwd-input').value.trim();
  }

  private updateDirUse(): void {
    const use = document.getElementById('dir-use') as HTMLButtonElement | null;
    if (use) use.disabled = !this.directoryReady();
  }

  /** 侧栏按钮上的路径展示。家目录缩写不在客户端做：桥没有暴露 home，
      * 而猜一个前缀会把别人的路径改错。超长路径由 CSS 做左省略，
      * 保留最有信息量的尾部（与 Pi Web 的 PathLabel 同一手法）。 */
  private displayPath(path: string): string {
    return path || '新建会话';
  }

  /** 侧栏「新建会话」按钮显示当前工作目录。 */
  private syncNewSessionButton(): void {
    const label = document.getElementById('new-session-cwd');
    const button = document.getElementById('new-session-btn');
    if (!label || !button) return;
    label.textContent = this.displayPath(this.cwd);
    button.title = this.cwd || '新建会话';
  }
  private async send(): Promise<void> {
    const input = el<HTMLTextAreaElement>('prompt'); const message = input.value.trim();
    if (!message) return;
    // trim 不会去掉零宽/格式字符：整条只有它们时消息会渲染成一个空白气泡，
    // 这里明确拒绝（而不是静默——按钮是亮的，用户需要知道为什么没发出去）。
    if (!hasVisibleText(message)) { this.notify('内容只有不可见字符，未发送。', 'warning'); return; }
    if (this.sending) {
      // 发送进行中再次提交：不能静默吞掉——用户无法区分「上一条仍在发」与
      // 「点击没反应」。注意可达路径是**回车提交**：disabled 的按钮不会派发
      // click，但表单提交不受按钮禁用限制，所以「灰着按钮再敲回车」会走到这里。
      // 输入内容保持原样，用户不会丢掉刚写的东西。
      this.notify('上一条消息仍在发送中，请稍候。', 'warning');
      return;
    }
    // sending 置真之后到 finally 之间不得有未受保护的取值：一旦抛错，
    // sending 会永久为真，按钮永久置灰，之后每次点击都被静默吞掉。
    this.sending = true; this.updateControls();
    // 排队意图必须在 ensureWorker 之前取：它会刷新会话状态，
    // 而刷新会把单选按钮重置成 Pi 的当前值，晚一步读就丢了用户的选择。
    const busy = this.run !== 'idle';
    const queuedKind = busy ? this.queueKind() : undefined;
    const selectedModel = this.selectedModel();
    try {
      const scope = this.scope.current$(); const draftKey = this.draftKey();
      const activeScope = await this.ensureWorker(scope);
      if (!activeScope.alive()) {
        // 会话已切换：不能把消息投到新会话（U13），也不静默丢弃。
        // 抛出而不是 return，让 catch 统一给出可见提示；输入与附件未清空。
        throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
      }
      if (!busy) {
        if (selectedModel) {
          await this.request('session.set_model', { provider: selectedModel.provider, modelId: selectedModel.id }, activeScope.sessionId);
          if (!activeScope.alive()) throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
          this.modelIntent = undefined; this.modelUnavailable = false;
          this.selectModel(selectedModel.provider, selectedModel.id);
        } else if (this.modelUnavailable) {
          throw new Error('当前会话没有可用模型。请配置模型或从列表中选一个可用模型后重试；消息仍保留在输入框。');
        }
        // 「自动」不发送 set_thinking：那是「不覆盖」的语义，不是某个具体等级。
        const level = this.thinkingChoice();
        if (level) {
          await this.request('session.set_thinking', { level }, activeScope.sessionId).catch(() => {});
          if (!activeScope.alive()) throw new Error('会话已切换，本条消息未发送；内容已保留，请手动重发。');
        }
        this.live.begin(message); this.setRun('running'); el('welcome').hidden = true;
      }
      const images = this.attachments;
      // 发送前按 base64 总量校验：超出预算时超限帧会直接断开 WS 连接，
      // 那比一条可读的错误提示糟得多（U05）。
      const wireBytes = images.reduce((sum, item) => sum + item.data.length, 0);
      if (wireBytes > WIRE_BUDGET) {
        throw new Error(`附件总体积超过 ${Math.floor(WIRE_BUDGET / 1024 / 1024)} MB，请减少图片或压缩后重试。`);
      }
      // 目标会话固定为发起时那一个：等待期间切换也不改投（U13）。
      if (busy && queuedKind) await this.sendQueued(message, queuedKind, images, activeScope);
      else await this.request('session.prompt', { text: message, ...(images.length ? { images: toWire(images) } : {}) }, activeScope.sessionId);
      // 消息已从跳转后的位置发出：分叉落定，编辑横幅可以收了。
      el('edit-scope').hidden = true;
      if (!activeScope.alive()) return;
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
        // 提示仍保留到状态核对结束（既有语义）；重订与重读走同一合并窗口，
        // 订阅被反复摘除时不得形成高频循环（见 runResync）。
        this.subscribed = '';
        if (this.run !== 'idle') this.notice(SUBSCRIPTION_CHECK_NOTICE);
        if (this.resyncDeferred()) return;
        this.runResync();
      }
      return;
    }
    const event = message as EventMessage;
    if (!this.cursor.accept(event.epoch, event.seq)) return;
    if (event.event === 'worker.exited') { this.setRun('idle'); return; }
    if (event.event === 'bridge.event_omitted') {
      // 事件因体积被省略：流的连续性已断，别等到下一次 settled（U15）。
      this.resyncFromStream('有事件因体积被省略，已重新读取历史以补齐内容。');
      return;
    }
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
    if ([ABORT_PENDING_NOTICE, SUBSCRIPTION_CHECK_NOTICE].includes(el('connection-notice').textContent ?? '')) this.notice('');
    const id = this.sessionId;
    await this.refreshHistory();
    if (id !== this.sessionId) return;
    // 刷新期间排队消息可能已经启动了下一轮（settled → 队列续跑之间没有
    // 用户操作）：此时清实时层会把新一轮刚画上的内容抹掉，是双重显示与
    // 「空白用户消息」的来源之一。只有仍空闲时才清。
    if (this.run === 'idle' && el('turns').querySelector('[data-turn-id]')) this.live.clear();
    this.refreshSessions();
    await this.refreshUsage(this.scope.current$());
  }
  // gotoLeaf 查看指定分支。leafId 为空表示回到磁盘上可恢复的当前分支。
  // 这只是查看，不改 Pi 的状态；要真正确认一个分支仍然走「从此处分支」。
  private gotoLeaf(leafId: string): void {
    if (!this.sessionId || !this.diskSession) return;
    // 与 refreshHistory 同一条通道：归属登记、in-flight 合流与冷却窗口
    // 都复用，分支视图不会与主历史并发交换（两个响应都会覆盖 #turns）。
    void this.refreshHistory(leafId)
      .then(() => { if (leafId) this.notify(`已切换到分支 ${leafId.slice(0, 8)} 的视图`); })
      .catch((error) => this.fail(error));
  }
  // forkFrom 是「新建会话」的唯一实现：回合上的按钮与分支导航面板都走
  // 这里。曾经两处各写一遍，后写的那份还少了对话框关闭，于是从面板
  // fork 成功后弹窗不消失。它让 Pi 把所选位置之前的历史复制进一个新
  // 会话文件，当前会话不受影响；要留在原会话继续编辑请用 editHere。
  private async forkFrom(entryId: string): Promise<void> {
    if (!entryId) { this.notify('缺少要分支的条目 ID', 'warning'); return; }
    try {
      const result = record(await this.command('session.fork', { entryId }));
      const id = text(result.sessionId);
      if (!id) { this.notify('Pi 没有返回新会话 ID', 'warning'); return; }
      this.selectSession(id, this.cwd, '分支会话', true, '', result.persisted !== false);
      const input = el<HTMLTextAreaElement>('prompt');
      input.value = text(result.text);
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.focus();
      closeDialog('branch-dialog');
    } catch (error) { this.fail(error); }
  }
  // 低频会话结构操作（「从此处编辑」的会话内跳转、删除会话）在独立
  // 分块里按需加载（与 branch/models 同一策略），不占首屏预算。
  private async sessionAction(kind: string, button: HTMLElement): Promise<void> {
    const { run } = await import('./session-actions');
    await run(kind, {
      command: this.command.bind(this),
      request: (m, p) => this.request(m, p, ''),
      refreshHistory: this.refreshHistory.bind(this),
      selectSession: this.selectSession.bind(this),
      refreshSessions: this.refreshSessions.bind(this),
      fail: this.fail.bind(this),
      sessionId: this.sessionId, cwd: this.cwd,
    }, button);
  }
  private async refreshHistory(leafId = ''): Promise<void> {
    if (!this.sessionId || !this.diskSession) return;
    if (this.historyLoading === this.sessionId) {
      // 同会话已有请求在飞：不并发（多个响应都会交换，视图会来回跳）。
      // 记下待补，完成后按最新状态再读一次，保证最后一次意图落地。
      this.historyRefreshPending = true;
      return;
    }
    const id = this.sessionId; this.historyLoading = id;
    // 归属随请求一起登记，beforeSwap 才能拒绝旧代次的响应（U17/U18）。
    this.pendingHistory = { sessionId: id, epoch: this.scope.epoch };
    const query = leafId ? `?leafId=${encodeURIComponent(leafId)}` : '';
    try { await window.htmx.ajax('get', `/ui/sessions/${encodeURIComponent(id)}/history${query}`, { target: '#turns', swap: 'innerHTML' }); }
    finally {
      if (this.historyLoading === id) this.historyLoading = '';
      if (this.historyRefreshPending && this.sessionId === id) {
        this.historyRefreshPending = false;
        void this.refreshHistory().catch((err) => this.fail(err));
      }
    }
  }
  private refreshSessions(): void {
    // 搜索结果与普通列表共用 #session-list；刷新列表说明已经退出搜索。
    this.searchQuery = '';
    window.htmx.trigger(document.body, 'sessions-refresh');
  }
  private markSelected(): void {
    for (const link of document.querySelectorAll<HTMLElement>('[data-session]')) {
      link.classList.toggle('selected', link.dataset.session === this.sessionId);
      if (link.dataset.session === this.sessionId && !this.cwd) { this.cwd = link.dataset.cwd ?? ''; this.sessionTitle = link.dataset.title ?? '会话'; }
    }
    el('session-count').textContent = String(document.querySelectorAll('[data-session]').length);
    // 直接打开 ?session=…（刷新、分享链接、从别处跳回）不会经过 selectSession，
    // 侧栏文件树就停在空路径上——服务端回退到第一个工作区根，列出的不是这个
    // 会话的目录。这里补一次，只在文件区还没目录时才推，不覆盖用户手动浏览的位置。
    const filesPath = document.getElementById('files-path') as HTMLInputElement | null;
    if (filesPath && !filesPath.value && this.cwd) {
      // 有 Workspace 就走它的 setCwd：它同时更新自己的「当前目录」，
      // 否则它的迟到响应守卫会把这次响应当成旧目录的（见 workspace.ts）。
      if (this.workspace) this.workspace.setCwd(this.cwd);
      else { filesPath.value = this.cwd; window.htmx.trigger(document.body, 'files-refresh'); }
    }
  }
  private async search(query: string): Promise<void> {
    if (!query) { this.refreshSessions(); return; }
    // 搜索结果是「数据 → HTML」，交给桥渲染（/ui/search），
    // 前端只用 htmx 把片段换进去。之前在这里用 createElement 搭
    // 列表，等于把后端的排版抄了一份。
    this.searchQuery = query;
    el<HTMLInputElement>('search-query').value = query;
    window.htmx.trigger(document.body, 'search-refresh');
  }
  /** finishSearch 在片段落地后清理：输入框已被改写说明结果不再对应当前词。 */
  private finishSearch(): void {
    if (el<HTMLInputElement>('session-search').value.trim() !== this.searchQuery) this.refreshSessions();
  }
  private async refreshThinking(selected?: string): Promise<void> {
    if (!this.sessionId) return; const id = this.sessionId;
    const levels = await this.request<string[]>('session.thinking_levels');
    if (id !== this.sessionId) return;
    const select = el<HTMLSelectElement>('thinking-select');
    // “自动”不是 Pi 的等级，而是“不覆盖”的 UI 语义：不发送 set_thinking，
    // 由 Pi 按 settings 的 defaultThinkingLevel 与模型自身能力决定。
    const remembered = this.thinkingBySession.get(id);
    const current = selected ?? remembered ?? select.value;
    select.replaceChildren(new Option('自动', ''), ...levels.map((level) => new Option(level, level)));
    select.value = levels.includes(current) ? current : '';
    select.disabled = this.run !== 'idle' || !levels.length;
  }
  // 思考强度按会话记住上次选择：Pi 不提供“未设置”的读回字段，
  // 界面只能用本机会话级偏好表达“自动”，不能假装知道 Pi 的实时值。
  private thinkingChoice(): string { return el<HTMLSelectElement>('thinking-select').value; }
  // 顶栏面板按需加载：首屏不承担这段代码，点击/提交时才 import。
  private topbar(): Promise<typeof import('./topbar')> { return import('./topbar'); }
  private toggleTopPanel(target: string, force?: boolean): void {
    void this.topbar().then((m) => m.toggle(this.host, target, force)).catch((err) => this.fail(err));
  }
  // topbarHost 把顶栏面板需要的最小能力交给独立模块，避免把面板逻辑留在首屏包内。
  /** title 暴露当前会话名，供测试与重命名流程读取。 */
  title(): string { return this.sessionTitle; }

  private get host(): TopbarHost {
    return {
      sessionId: () => this.sessionId,
      cwd: () => this.cwd,
      busy: () => this.run !== 'idle',
      modelLabel: () => this.currentModel ? `${this.currentModel.id} · ${this.currentModel.provider}` : (this.modelUnavailable ? '当前不可用' : '未选择'),
      contextWindow: () => this.contextWindow,
      setContextWindow: (value) => { this.contextWindow = value; },
      thinking: () => el<HTMLSelectElement>('thinking-select').value,
      preset: () => el<HTMLSelectElement>('tool-preset-quick').value,
      setTitle: (title) => { this.sessionTitle = title; },
      setPreset: (value) => { const node = document.getElementById('tool-preset-quick') as HTMLSelectElement | null; if (node) node.value = value; },
      presetKey: () => this.queueChoiceKey(),
      presetBySession: () => this.toolPresetBySession,
      request: (method, params, session) => this.request(method, params, session),
      notify: (message, kind) => this.notify(message, kind),
      fail: (error) => this.fail(error),
      refreshState: () => this.refreshState(),
    };
  }
  private selectedModel(): { provider: string; id: string } | undefined {
    const option = el<HTMLSelectElement>('model-select').selectedOptions[0];
    return option?.dataset.provider && option.dataset.modelId ? { provider: option.dataset.provider, id: option.dataset.modelId } : undefined;
  }
  private applyStateModel(state: State): void {
    if (!('model' in state)) return;
    this.currentModel = knownModel(state.model) ? { ...state.model, name: state.model.name || state.model.id } : undefined;
    this.modelUnavailable = !this.currentModel;
    // 上下文窗口来自模型元数据，与用量统计是否可用无关：统计失败时也要能显示它。
    const window = (state.model as { contextWindow?: number } | undefined)?.contextWindow;
    if (typeof window === 'number' && window > 0) this.contextWindow = window;
    this.renderModel();
  }
  private renderModel(): void {
    const select = el<HTMLSelectElement>('model-select');
    select.querySelectorAll('option[data-model-status]').forEach((option) => option.remove());
    if (this.modelIntent) {
      const option = Array.from(select.options).find((item) => item.dataset.provider === this.modelIntent?.provider && item.dataset.modelId === this.modelIntent?.id);
      if (option) { select.value = option.value; select.title = option.title || option.textContent?.trim() || ''; this.updateControls(); return; }
    }
    if (this.currentModel) {
      this.selectModel(this.currentModel.provider, this.currentModel.id, this.currentModel.name);
    } else if (this.historicalModel || this.modelUnavailable) {
      const model = this.historicalModel;
      const label = model ? `${model.id}（${this.modelUnavailable ? '不可用' : '历史'}）` : '当前模型不可用';
      const option = new Option(label, '__model-status__');
      option.disabled = true; option.dataset.modelStatus = 'true'; select.insertBefore(option, select.firstChild);
      select.value = option.value;
    } else {
      select.value = '';
    }
    select.title = this.historicalModel && !this.currentModel
      ? `${this.historicalModel.provider}/${this.historicalModel.id} · ${this.modelUnavailable ? '历史模型，当前不可用' : '历史模型，待启动确认'}`
      : (select.selectedOptions[0]?.title || select.selectedOptions[0]?.textContent?.trim() || '');
    this.updateControls();
  }
  private selectModel(provider: string, id: string, name = id): void {
    this.currentModel = { provider, id, name };
    const select = el<HTMLSelectElement>('model-select');
    let option = Array.from(select.options).find((item) => item.dataset.provider === provider && item.dataset.modelId === id);
    if (!option) { option = new Option(name, `${provider}/${id}`); option.title = `${provider}/${id}`; option.dataset.provider = provider; option.dataset.modelId = id; option.dataset.runtimeModel = 'true'; select.add(option); }
    select.value = option.value;
    select.title = option.title || option.textContent?.trim() || '';
  }
  private async changeModel(): Promise<void> {
    const selected = this.selectedModel();
    this.modelIntent = selected;
    if (!selected || !this.sessionId) { this.renderModel(); return; }
    const scope = this.scope.current$();
    try {
      await this.command('session.set_model', { provider: selected.provider, modelId: selected.id }, scope);
      if (!scope.alive()) return;
      this.modelIntent = undefined; this.modelUnavailable = false;
      this.selectModel(selected.provider, selected.id);
      await this.refreshState(scope);
    } catch (error) {
      if (scope.alive()) { this.modelIntent = undefined; this.renderModel(); }
      throw error;
    }
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
    // 归属在回复前固定：回执等待期间切了会话，迟到的成功与错误
    // 都不得改动新会话的对话框、状态行或提示（U18）。
    const scope = this.scope.current$();
    const params: Record<string, unknown> = { id: dialog.dataset.dialogId };
    if (!button || button.name === 'cancelled') params.cancelled = true;
    else if (button.name === 'confirmed') params.confirmed = button.value === 'true';
    else params.value = new FormData(form).get('value') ?? '';
    for (const btn of form.querySelectorAll('button')) btn.disabled = true;
    try { await this.request('session.ui_response', params); if (!scope.alive()) return; dialog.close(); dialog.remove(); await this.refreshDialogs(); await this.reconcile(); }
    catch (error) { if (!scope.alive()) return; this.fail(error); for (const btn of form.querySelectorAll('button')) btn.disabled = false; }
  }
  private showCommands(): void {
    const menu = el('command-menu'); const value = el<HTMLTextAreaElement>('prompt').value;
    menu.replaceChildren(); menu.hidden = !value.startsWith('/') || value.includes(' ') || !this.commands.length;
    if (menu.hidden) return;
    for (const command of this.commands.filter((c) => c.name.startsWith(value.slice(1))).slice(0, 20)) { const button = document.createElement('button'); button.type = 'button'; button.dataset.command = command.name; button.textContent = `/${command.name}  ${command.description}`; menu.append(button); }
  }
  private onClick(event: MouseEvent): void {
    const target = event.target as Element;
    // 顶栏面板内部的瞬时交互（工具选中项、复制按钮）。
    // 只用到时才加载：这块逻辑不在首次交互路径上，没必要占首屏预算。
    if (target.closest('[data-tool-select],[data-copy-value]')) {
      void import('./panels').then((module) => module.panelClick(event)).catch((error) => this.fail(error));
      return;
    }
    // 分支面板的两个动作由片段里的 data-branch-* 声明，交给模块翻译；
    // 模块可能还没加载（面板未开过），所以先问一句。
    if (this.branch?.handleClick(event)) return;
    const link = target.closest<HTMLElement>('[data-session]');
    if (link) { event.preventDefault(); this.selectSession(link.dataset.session ?? '', link.dataset.cwd ?? '', link.dataset.title ?? '会话', true, link.dataset.entryId ?? ''); return; }
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
      case 'settings': this.openSettings('general'); break;
      case 'branch': {
        if (!this.branch) {
          const { BranchNavigator } = await import('./branch');
          if (this.abort.signal.aborted) return;
          this.branch ??= new BranchNavigator(this.bridge, (err) => this.fail(err), (leafId) => this.gotoLeaf(leafId), (entryId) => void this.forkFrom(entryId), () => this.sessionId, this.abort.signal);
        }
        openDialog('branch-dialog');
        await this.branch.open();
        break;
      }
      case 'branch-refresh': await this.branch?.refresh(); break;
      case 'branch-current': this.gotoLeaf(''); break;
      case 'models-edit': {
        if (!this.models) { const { ModelsEditor } = await import('./models'); if (this.abort.signal.aborted) return; this.models ??= new ModelsEditor(this.bridge, (err) => this.fail(err)); }
        openDialog('models-dialog');
        await this.models.open();
        break;
      }
      case 'models-toggle-secret': {
        const input = document.getElementById('mp-key') as HTMLInputElement | null;
        if (!input) break;
        const show = input.type === 'password';
        input.type = show ? 'text' : 'password';
        button.textContent = show ? '隐藏' : '显示';
        break;
      }
      case 'models-delete-provider': {
        if (!this.models) break;
        if (!confirm('删除整个供应商会同时移除它下面的全部模型，确定？')) break;
        this.models.deleteProvider();
        break;
      }
      case 'models-delete-model': {
        if (!this.models) break;
        this.models.deleteModel();
        break;
      }
      case 'models-reload': await this.models?.reload(); break;
      case 'models-save': await this.models?.save(); break;
      case 'models-discover': await this.models?.discover(); break;
      case 'models-test': await this.models?.test(); break;
      case 'models-catalog': await this.models?.catalog(); break;
      case 'session-menu': {
        el<HTMLInputElement>('session-name').value = this.sessionTitle;
        openDialog('session-dialog');
        if (this.sessionId) await this.refreshState();
        break;
      }
      case 'abort':
        await this.request('session.abort');
        if (this.run !== 'idle') this.notice(ABORT_PENDING_NOTICE);
        break;
      case 'rename': this.sessionTitle = el<HTMLInputElement>('session-name').value; await this.command('session.set_name', { name: this.sessionTitle }); this.refreshSessions(); break;
      case 'compact': this.setRun('compacting'); try { await this.command('session.compact'); await this.refreshHistory(); } finally { await this.reconcile(); } break;
      case 'clone': { const result = await this.command<{sessionId:string}>('session.clone'); this.selectSession(result.sessionId, this.cwd, '克隆会话'); this.refreshSessions(); break; }
      case 'fork': await this.forkFrom(button.dataset.entryId ?? ''); break;
      case 'edit-here': await this.sessionAction('edit', button); break;
      case 'stop': await this.request('session.stop', { force: false }); this.setRun('idle'); this.notice('工作进程已释放，历史保留在磁盘。'); break;
      case 'delete': await this.sessionAction('delete', button); break;
      case 'copy-turn': {
        // 一个回合可能有多段正文（工具之间穿插说明）；全部按顺序复制，
        // 只取第一段会把中间的解释丢掉。
        const turn = button.closest('.turn');
        const parts = [...(turn?.querySelectorAll('.turn-assistant .bubble') ?? [])].map((node) => node.textContent ?? '').filter(Boolean);
        await navigator.clipboard.writeText(parts.join('\n\n')); this.notify('已复制'); break;
      }
      case 'commands': this.commands = (await this.command<Record<string,unknown>[]>('session.commands')).map((c) => ({name: text(c.name), description: text(c.description)})); el<HTMLTextAreaElement>('prompt').value = '/'; this.showCommands(); el('prompt').focus(); break;
      case 'export': {
        // 用完整会话 ID，截断只会得到 "history-" 这种没有辨识度的名字。
        const name = `session-${this.sessionId.slice(0, 64)}.html`;
        // 导出是磁盘投影：这里刻意不走 command()（它会先 ensureWorker），
        // 只看历史不该拉起 Pi 进程（B76）。
        const result = await this.request<{ path: string }>('session.export_html', { fileName: name });
        const file = text(result.path).split('/').pop() || name;
        this.notify('已导出，开始下载。');
        // 走普通导航而不是 fetch：需要浏览器弹出下载，且要带登录 Cookie。
        // 用文档基地址解析：`location.assign` 不受 <base href> 影响，
        // 写根绝对路径在设备前缀形态下会跳出前缀，下载 404（B54）。
        location.assign(absoluteUrl(`ui/exports/${encodeURIComponent(file)}`).toString());
        break;
      }
      case 'abort-retry': await this.command('session.abort_retry'); this.notify('已请求中止重试。'); await this.reconcile(); break;
      case 'attach': el<HTMLInputElement>('attach-input').click(); break;
      case 'workspace': {
        const panel = el('workspace-panel'); panel.hidden = !panel.hidden; el('workbench').dataset.rightPanel = panel.hidden ? 'closed' : 'open';
        document.querySelector<HTMLElement>('[aria-controls="workspace-panel"]')?.setAttribute('aria-expanded', String(!panel.hidden));
        if (!panel.hidden) { this.workspace?.setCwd(this.cwd); await this.workspace?.open(); } break;
      }
    }
  }
  // refreshQueueState 反映 Pi 可读回的排队模式与自动压缩。
  // 自动重试没有读回字段，只在用户本次操作时更新，不清空。
  private refreshQueueState(state: State): void {
    const steering = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="steering"]');
    const followUp = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]');
    if (steering && followUp) {
      const chosen = this.queueChoiceBySession.get(this.queueChoiceKey());
      if (chosen) (chosen === 'followUp' ? followUp : steering).checked = true;
      else if (state.followUpMode !== undefined) {
        (state.followUpMode === 'one-at-a-time' ? followUp : steering).checked = true;
      }
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
      wireLazy(() => this.sessionId, this.abort.signal);
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
    // FileList 在清空选择器或 drop 事件结束后可能立即变空；排队前快照。
    const batch = Array.from(files);
    const previous = this.attachQueue;
    // 无论上一批成功与否都要释放队列，否则一次失败会永久堵住后续附件。
    const run = previous.then(() => this.attachBatch(batch)).catch((error) => { this.fail(error); });
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
  private queueChoiceKey(): string { return this.sessionId || `new:${this.cwd}`; }
  private saveCurrentDraft(): void { saveDraft(this.draftKey(), el<HTMLTextAreaElement>('prompt').value); }
  private setConnection(online: boolean): void { el('conn-state').textContent = online ? '已连接' : '未连接'; el('conn-state').className = `state state-${online ? 'online' : 'offline'}`; if (online) this.notice(''); this.updateControls(); }
  private setRun(state: RunState): void { this.run = state; el('session-state').textContent = {idle:'就绪',running:'运行中',retrying:'重试中',compacting:'压缩中',waiting_input:'等待确认'}[state]; this.updateControls(); }
  private updateControls(): void { el<HTMLButtonElement>('send-button').disabled = !this.bridge.connected || this.sending || !el<HTMLTextAreaElement>('prompt').value.trim() || (this.modelUnavailable && !this.selectedModel()); el('abort-button').hidden = this.run === 'idle'; const hint = el('queue-hint'); const busy = this.run !== 'idle'; hint.hidden = !busy; if (busy) hint.textContent = this.queueKind() === 'steering' ? '本轮结束后插入指令' : '排到队列末尾，本轮完成后追加'; el<HTMLSelectElement>('model-select').disabled = busy; el<HTMLSelectElement>('thinking-select').disabled = busy || !this.sessionId; }
  // queueKind 读会话对话框里的 radio；缺省 steering，与 Pi 的默认一致。
  // 取值必须与桥的协议一致：steering/followUp，不是 steer。
  private queueKind(): 'steering' | 'followUp' { return document.querySelector<HTMLInputElement>('input[name="queue-kind"]:checked')?.value === 'followUp' ? 'followUp' : 'steering'; }
  // sendQueued 在运行中发送：先把模式同步给桥，再带 streamingBehavior 提交。
  // 不先同步的话，用户改了 radio 但桥仍是旧模式，行为与界面显示不一致。
  private async sendQueued(text: string, kind: 'steering' | 'followUp', images: Attachment[], scope: Scope): Promise<void> {
    // 排队模式与消息都必须发往发起时那个会话，不能跟着 this.sessionId 漂移。
    await this.request('session.set_queue_mode', { kind, mode: kind === 'steering' ? 'all' : 'one-at-a-time' }, scope.sessionId);
    // 队列配置叫 steering，Pi prompt 的 streamingBehavior 则叫 steer。
    await this.request('session.prompt', { text, streamingBehavior: kind === 'steering' ? 'steer' : 'followUp', ...(images.length ? { images: toWire(images) } : {}) }, scope.sessionId);
  }
  private notice(message: string): void { el('connection-notice').textContent = message; el('connection-notice').hidden = !message; }
  private notify(message: string, kind = 'info'): void { void import('./toast').then(({showToast}) => showToast(message, kind === 'error' ? 'error' : kind === 'warning' ? 'warning' : 'info')); }
  private fail(error: unknown): void { const message = error instanceof Error ? error.message : '操作失败'; this.notify(message, 'error'); this.notice(message); }
  dispose(): void { this.saveCurrentDraft(); this.abort.abort(); clearInterval(this.poll); clearTimeout(this.searchTimer); clearTimeout(this.resyncTimer); this.live.dispose(); this.models?.dispose(); this.workspace?.dispose(); this.bridge.dispose(); }
}
