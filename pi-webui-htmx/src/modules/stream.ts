// 这里只维护当前正在输出的一轮；历史仍由桥分页渲染。
export function record(value: unknown): Record<string, unknown> { return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
export function text(value: unknown): string { return typeof value === 'string' ? value : ''; }
export function messageText(value: unknown): string {
  const content = record(value).content;
  if (typeof content === 'string') return content;
  return Array.isArray(content) ? content.filter((block) => record(block).type === 'text').map((block) => text(record(block).text)).join('\n') : '';
}
export type RunState = 'idle' | 'running' | 'retrying' | 'compacting' | 'waiting_input';
export function runStateAfter(state: RunState, event: Record<string, unknown>): RunState {
  switch (event.type) {
    case 'agent_start': return 'running';
    case 'agent_settled': return 'idle';
    case 'auto_retry_start': return 'retrying';
    case 'compaction_start': case 'auto_compaction_start': return 'compacting';
    case 'extension_ui_request': return ['confirm', 'select', 'input', 'editor'].includes(text(event.method)) ? 'waiting_input' : state;
    // agent_end 后可能重试或继续运行，不把它判作最终结束。
    default: return state;
  }
}
export class EventCursor {
  epoch = ''; seq = 0;

  /**
   * begin 用订阅确认里的 epoch/seq 重设游标。
   *
   * **切换 epoch 只走这里**：确认帧里的 epoch 是桥对「这条订阅属于哪个
   * 工作进程」的权威回答，而事件流里出现的其它 epoch 一律视为上一个
   * worker 的延迟帧（U16）。
   */
  begin(epoch: string, seq = 0): void {
    if (!epoch) return;
    const next = Number.isSafeInteger(seq) && seq > 0 ? seq : 0;
    // 同 epoch 的确认不得让游标倒退：重订阅等竞态下迟到的确认
    // 若把 seq 拉回去，已应用的事件会被重新接受一遍（U16）。
    if (this.epoch === epoch && next < this.seq) return;
    this.epoch = epoch;
    this.seq = next;
  }

  accept(epoch: string, seq: number): boolean {
    if (!epoch || !Number.isSafeInteger(seq) || seq <= 0) return false;
    // 还没有权威 epoch 时以第一条事件为准（例如页面直接接上已有订阅）。
    if (!this.epoch) { this.epoch = epoch; this.seq = seq; return true; }
    // 不同 epoch 一律丢弃。旧实现遇到新 epoch 就把 seq 归零并接收，
    // 于是上一个 worker 的延迟帧能把游标切回去，旧事件被当成新的应用。
    if (epoch !== this.epoch) return false;
    if (seq <= this.seq) return false;
    this.seq = seq; return true;
  }

  reset(): void { this.epoch = ''; this.seq = 0; }
}
const MAX_LIVE_CHARS = 200_000;

// THINKING_LIMIT 限制思考块的实时字符数，超出部分不再追加。
const THINKING_LIMIT = 40_000;

// 实时正文段的 markdown 渲染是「节流 + 整段重渲」：marked 解析整段文本，
// 每个增量都跑一遍在长段上会拖住主线程；不渲染则用户在整个运行期间看到的
// 都是原始 markdown（`**粗体**`、表格语法），要等结算重读历史才正常。
// 节流到 350ms 一次，兼顾「看起来是富文本」与开销。
const LIVE_MARKDOWN_INTERVAL_MS = 350;
// 超过这个长度放弃实时渲染（保留纯文本）：整段重渲在超长文本上不值得，
// 结算后历史路径仍会完整渲染。
const LIVE_MARKDOWN_LIMIT = 60_000;

// 实时工具行的预览：bash 显示命令首行，文件工具显示路径。
// 运行期间用户必须能看到工具在做什么——此前对 bash 一律返回空，
// 整个运行期只见「bash + 工具已开始执行」，结算重读历史后才看到命令与输出。
const TOOL_PREVIEW_CHARS = 120;
function toolPreview(toolName: string, args: Record<string, unknown>): string {
  if (toolName === 'bash') {
    // 多行命令（heredoc、脚本）只取第一行进预览，全文在展开详情里。
    const first = text(args.command).split('\n')[0] ?? '';
    return first.length > TOOL_PREVIEW_CHARS ? first.slice(0, TOOL_PREVIEW_CHARS) + '…' : first;
  }
  const fileTools = new Set(['read', 'edit', 'write', 'grep', 'find', 'ls']);
  return fileTools.has(toolName) ? text(args.path).slice(0, 240) : '';
}

// 实时工具输出的保留上限：只留尾部（命令输出的错误通常在末尾），
// 超限时前缀标明省略。结算后的历史视图仍会显示全部。
const TOOL_OUTPUT_LIMIT = 20_000;
function toolOutput(body: string): string {
  if (body.length <= TOOL_OUTPUT_LIMIT) return body;
  return `[…前文已省略]\n${body.slice(-TOOL_OUTPUT_LIMIT)}`;
}

// toolDetailSeed 是工具行刚创建时展开详情的初始内容：展开即可看到完整调用
// （bash 全文命令 / 文件路径），而不是一句占位符。
function toolDetailSeed(toolName: string, args: Record<string, unknown>): string {
  if (toolName === 'bash') return text(args.command) || '工具已开始执行';
  const path = text(args.path);
  return path || '工具已开始执行';
}

// droppedMarker 是提供商把模型文本丢弃后留下的占位标记（实测只有 "[dropped ]"）。
// 它没有语义，显示出来只会像乱码；只在文本结尾剥离，不碰中间疑似字样。
const droppedMarker = /\s*\[dropped\s*\]\s*$/;

// visibleChars 去掉空白与零宽/格式字符后是否还有可见内容。
// 用于拦掉「整条消息都是不可见字符」的发送（否则渲染出一个空白气泡）。
export function hasVisibleText(value: string): boolean {
  return /[^\s\u200b\u200c\u200d\u2060\ufeff]/.test(value);
}
export function stripDroppedMarker(value: string): string {
  return value.replace(droppedMarker, '');
}

export class LiveView {
  private frame = 0;
  // scheduled 与 frame 分开：rAF 回调里会重置 frame，若把 frame 本身当
  // 「已排程」标志，同步执行的回调（测试桩/某些实现）会让标志残留，
  // 之后的增量就再也调度不到刷新。
  private scheduled = false;
  private chunks = '';
  private rendered = 0;
  private truncated = false;
  private thinkingChars = 0;
  private thinkingTail = '';
  private previewAt: number | undefined;
  private previewTimer: ReturnType<typeof setTimeout> | undefined;
  // 实时时间线按「工作段 / 正文段」交替：模型会在工具之间穿插正文，
  // 正文必须落在它出现的位置，而不是全部堆在末尾。每段工作有独立的
  // 标题行（状态 + 思考预览）与项容器，活动信号只挂在最新一段上。
  private segment: 'work' | 'text' | null = null;
  private work: { group: HTMLDetailsElement; status: HTMLElement; preview: HTMLElement; items: HTMLElement; tools: number } | null = null;
  private text: HTMLElement | null = null;
  // 当前正文段的原始文本（markdown 源）。DOM 里可能已经是渲染后的 HTML，
  // 不能反读 textContent 当源——渲染器会把 `**x**` 变成 `<strong>`。
  private textRaw = '';
  // mdRenderedFor 是「最近一次成功渲染时的源长度」，-1 表示尚未渲染。
  private mdRenderedFor = -1;
  private mdTimer: ReturnType<typeof setTimeout> | undefined;
  // 工具输出的 rAF 合帧：partialResult 可能每个输出块来一次，
  // 逐条写 DOM 在长输出下会造成持续的布局抖动。
  private pendingToolOutput = new Map<HTMLElement, string>();
  private toolFlushScheduled = false;
  private toolFrame = 0;
  constructor(private readonly root: HTMLElement) {}
  begin(userText?: string): void {
    this.clear(); this.root.hidden = false; this.root.dataset.running = 'true';
    this.showPending('正在处理…');
    const user = this.root.querySelector<HTMLElement>('#live-user');
    if (user) { user.textContent = userText ?? ''; user.hidden = !userText; }
  }
  event(event: Record<string, unknown>): void {
    if (event.type === 'message_start') {
      this.flush(); this.resetPreview();
    }
    if (event.type === 'message_update') {
      const delta = record(event.assistantMessageEvent);
      if (delta.type === 'text_delta') {
        this.resetPreview(); this.append(text(delta.delta));
      }
      if (delta.type === 'thinking_delta') this.appendThinking(text(delta.delta));
    }
    if (event.type === 'tool_execution_start') this.startTool(event);
    if (event.type === 'tool_execution_update') this.updateTool(event);
    if (event.type === 'tool_execution_end') this.endTool(event);
    if (event.type === 'message_end') {
      const message = record(event.message);
      if (message.stopReason === 'error') {
        this.resetPreview(); this.status('模型请求失败');
        this.append('\n[模型请求失败；完成后查看历史中的错误提示]');
      }
    }
  }
  finish(): void {
    this.flush(); this.resetPreview();
    // 收尾补一次渲染：结算提示期间最后一段正文以富文本呈现（随后被历史替换）。
    clearTimeout(this.mdTimer); this.mdTimer = undefined;
    void this.renderLiveMarkdown();
    if (this.work) { this.work.group.removeAttribute('data-active'); this.status('处理结束，正在读取历史…'); }
    else this.showPending('处理结束，正在读取历史…');
    delete this.root.dataset.running;
  }
  clear(): void {
    this.dispose(); this.rendered = 0; this.truncated = false; this.thinkingChars = 0;
    this.segment = null; this.work = null; this.text = null; this.textRaw = ''; this.mdRenderedFor = -1;
    const flow = this.root.querySelector('#live-flow');
    if (flow) flow.replaceChildren();
    // 用户气泡必须连同 hidden 一起复位：只清文字的话，「settled 清理之后
    // 又来一帧增量」（排队消息续跑、重连补发）会把实时层重新显示成一个
    // 空的用户气泡——用户看到的是「多出来一条空白用户消息」。
    const user = this.root.querySelector<HTMLElement>('#live-user');
    if (user) { user.textContent = ''; user.hidden = true; }
    const pending = this.root.querySelector<HTMLElement>('#live-pending');
    if (pending) { pending.textContent = ''; pending.hidden = true; }
    this.root.hidden = true; delete this.root.dataset.running;
  }
  dispose(): void {
    cancelAnimationFrame(this.frame); this.frame = 0; this.scheduled = false; this.chunks = '';
    cancelAnimationFrame(this.toolFrame); this.toolFrame = 0; this.toolFlushScheduled = false; this.pendingToolOutput.clear();
    clearTimeout(this.mdTimer); this.mdTimer = undefined;
    this.resetPreview();
  }
  // flowEl 取时间线容器；测试夹具不带 #live-flow 时退回 root 本身。
  private flowEl(): HTMLElement {
    const flow = this.root.querySelector<HTMLElement>('#live-flow');
    return flow ?? this.root;
  }
  private showPending(value: string): void {
    const el = this.root.querySelector<HTMLElement>('#live-pending');
    if (el) { el.textContent = value; el.hidden = false; }
  }
  private hidePending(): void {
    const el = this.root.querySelector<HTMLElement>('#live-pending');
    if (el) el.hidden = true;
  }
  private status(value: string): void {
    if (!this.work) return;
    this.work.status.textContent = this.work.tools ? `${value} · ${this.work.tools} 次工具调用` : value;
  }
  private resetPreview(): void {
    clearTimeout(this.previewTimer); this.previewTimer = undefined; this.previewAt = undefined; this.thinkingTail = '';
    if (this.work) this.work.preview.textContent = '';
  }
  private showPreview(): void {
    this.previewTimer = undefined; this.previewAt = Date.now();
    if (this.work) this.work.preview.textContent = this.thinkingTail.replace(/\s+/g, ' ').trim();
  }
  // ensureWork 返回当前工作段；当前段是正文（或还没有段）时新开一段。
  private ensureWork(): NonNullable<LiveView['work']> {
    if (this.segment === 'work' && this.work) return this.work;
    // 先落盘缓冲中的正文：它属于上一段，晚一步就会排到新工具之后。
    this.flush();
    const flow = this.flowEl();
    this.hidePending();
    const group = document.createElement('details');
    group.className = 'turn-process live-group';
    group.dataset.active = '';
    const summary = document.createElement('summary');
    summary.className = 'process-summary';
    const status = document.createElement('span'); status.className = 'live-status'; status.textContent = '正在处理…';
    const preview = document.createElement('span'); preview.className = 'process-preview live-preview';
    summary.append(status, preview);
    const body = document.createElement('div'); body.className = 'process-body';
    const items = document.createElement('div'); items.className = 'live-items';
    body.append(items);
    group.append(summary, body);
    flow.append(group);
    // 正文段到此结束：同一条流里的后续内容属于新的时间段。
    this.segment = 'work';
    this.work = { group, status, preview, items, tools: 0 };
    return this.work;
  }
  // ensureText 返回当前正文段；当前段是工作（或还没有段）时新开一段。
  private ensureText(): HTMLElement {
    if (this.segment === 'text' && this.text) return this.text;
    const flow = this.flowEl();
    this.hidePending();
    // 活动信号交给光标：旧的标题行不再扫光。
    this.work?.group.removeAttribute('data-active');
    const el = document.createElement('div');
    // markdown 类让渲染产物享受与历史正文相同的排版样式（p/表格/代码块）。
    el.className = 'bubble streaming-text markdown';
    flow.append(el);
    this.segment = 'text';
    this.text = el;
    this.textRaw = ''; this.mdRenderedFor = -1;
    return el;
  }
  private startTool(event: Record<string, unknown>): void {
    this.root.hidden = false; this.resetPreview();
    const name = text(event.toolName) || '工具';
    const work = this.ensureWork();
    work.tools++;
    this.status(`正在调用 ${name}`);
    if (work.tools > 100) return;
    const row = document.createElement('details'); row.className = 'tool-call live-tool'; row.dataset.state = 'running';
    // toolCallId 是 update/end 的归属键：并行工具各自的行必须精确对应，
    // 不能靠「最后一个运行中的行」猜（并行 bash 的输出会串行）。
    row.dataset.toolCallId = text(event.toolCallId);
    const summary = document.createElement('summary');
    const label = document.createElement('span'); label.className = 'tool-name'; label.textContent = name;
    const preview = document.createElement('span'); preview.className = 'tool-preview';
    const args = record(event.args);
    preview.textContent = toolPreview(name, args);
    summary.append(label, preview); row.append(summary);
    const detail = document.createElement('pre'); detail.className = 'tool-detail';
    // 初始详情给命令/路径本身，展开就能看到完整调用；输出到达后由
    // update/end 替换为累积输出。
    detail.textContent = toolDetailSeed(name, args);
    row.append(detail); work.items.append(row);
  }
  // findToolRow 按 toolCallId 定位工具行；缺 ID 时退回最后一个运行中的行。
  private findToolRow(callId: string): HTMLElement | null {
    const rows = this.flowEl().querySelectorAll<HTMLElement>('.live-tool');
    let fallback: HTMLElement | null = null;
    for (const row of rows) {
      if (row.dataset.state !== 'running') continue;
      fallback = row;
      if (callId && row.dataset.toolCallId === callId) return row;
    }
    return callId ? null : fallback;
  }
  // updateTool 处理累积输出的流式增量。partialResult 是累计文本（不是 delta），
  // 因此整体替换而不是追加；按 rAF 合帧写入，避免每个输出块都触发一次布局。
  private updateTool(event: Record<string, unknown>): void {
    const row = this.findToolRow(text(event.toolCallId));
    if (!row) return;
    const body = messageText(record(event.partialResult));
    if (!body) return;
    this.pendingToolOutput.set(row, toolOutput(body));
    if (this.toolFlushScheduled) return;
    this.toolFlushScheduled = true;
    this.toolFrame = requestAnimationFrame(() => {
      this.toolFlushScheduled = false;
      for (const [target, content] of this.pendingToolOutput) {
        const pre = target.querySelector('.tool-detail');
        if (pre) pre.textContent = content;
      }
      this.pendingToolOutput.clear();
    });
  }
  // endTool 收尾：写入最终输出、按 isError 切成功/失败配色（复用历史
  // tool-call[data-ok] 的同一套选择器）并清除运行态。
  private endTool(event: Record<string, unknown>): void {
    const row = this.findToolRow(text(event.toolCallId));
    if (!row) return;
    this.pendingToolOutput.delete(row);
    const body = messageText(record(event.result));
    if (body) {
      const pre = row.querySelector('.tool-detail');
      if (pre) pre.textContent = toolOutput(body);
    }
    row.dataset.state = 'done';
    row.dataset.ok = event.isError === true ? 'false' : 'true';
  }
  private append(delta: string): void {
    if (!delta || this.truncated) return;
    this.root.hidden = false;
    // 内容一到就收起等待提示：缓冲到下一帧才隐藏会让它多闪一下。
    this.hidePending();
    const remaining = MAX_LIVE_CHARS - this.rendered - this.chunks.length;
    if (delta.length > remaining) {
      this.chunks += delta.slice(0, Math.max(0, remaining)) + '\n[实时预览已达上限，完成后读取持久历史]'; this.truncated = true;
    } else this.chunks += delta;
    if (!this.scheduled) {
      this.scheduled = true;
      this.frame = requestAnimationFrame(() => { this.scheduled = false; this.flush(); });
    }
  }
  private flush(): void {
    cancelAnimationFrame(this.frame); this.frame = 0; this.scheduled = false;
    if (!this.chunks) return;
    const el = this.ensureText();
    this.textRaw += this.chunks; this.rendered += this.chunks.length; this.chunks = '';
    // 段尾的丢弃标记与「整段只有空白」都不该显示成气泡（min-height 会留一块空白）。
    if (this.textRaw.includes('[dropped')) this.textRaw = stripDroppedMarker(this.textRaw);
    const visible = this.textRaw.trim() !== '';
    el.hidden = !visible;
    if (!visible) return;
    // 已渲染过 markdown 的段落保留渲染视图，等下一次节流渲染整体替换——
    // 每帧写回纯文本会让富文本在流式期间反复闪回原始 markdown。
    if (this.mdRenderedFor < 0 || this.textRaw.length > LIVE_MARKDOWN_LIMIT) el.textContent = this.textRaw;
    this.scheduleLiveMarkdown();
  }
  private scheduleLiveMarkdown(): void {
    if (this.mdTimer !== undefined) return;
    this.mdTimer = setTimeout(() => { this.mdTimer = undefined; void this.renderLiveMarkdown(); }, LIVE_MARKDOWN_INTERVAL_MS);
  }
  private async renderLiveMarkdown(): Promise<void> {
    const el = this.text;
    if (!el || !el.isConnected) return;
    if (this.textRaw.length > LIVE_MARKDOWN_LIMIT || this.textRaw.trim() === '') return;
    if (this.mdRenderedFor === this.textRaw.length) return;
    const source = this.textRaw;
    try {
      const { safeMarkdown } = await import('./markdown');
      // 渲染期间可能已经切段或追加：只有仍是同一段、源未变时才写回。
      if (this.text !== el || !el.isConnected || this.textRaw !== source) { this.scheduleLiveMarkdown(); return; }
      el.innerHTML = await safeMarkdown(source);
      for (const link of el.querySelectorAll('a')) link.rel = 'noopener noreferrer';
      this.mdRenderedFor = source.length;
    } catch { /* 渲染失败保持纯文本；后续 flush 会继续用纯文本路径。 */ }
  }
  private appendThinking(delta: string): void {
    if (!delta) return;
    this.root.hidden = false;
    const work = this.ensureWork();
    this.status('思考中…');
    // 全文达到预览上限后，活动摘要仍跟随最新的思考，避免看起来停止工作。
    this.thinkingTail = (this.thinkingTail + delta).slice(-180);
    const elapsed = this.previewAt === undefined ? 1500 : Date.now() - this.previewAt;
    if (elapsed >= 1500) { clearTimeout(this.previewTimer); this.showPreview(); }
    else if (this.previewTimer === undefined) this.previewTimer = setTimeout(() => this.showPreview(), 1500 - elapsed);
    // 思考全文接着上一段写；中间出现工具行时另起一段（与历史侧同构，
    // 保持「时间戳 − 时长 → 时间戳」的先后可读）。
    let pre = work.items.lastElementChild;
    if (!(pre instanceof HTMLPreElement) || !pre.classList.contains('live-think')) {
      pre = document.createElement('pre'); pre.className = 'live-think'; work.items.append(pre);
    }
    if (this.thinkingChars >= THINKING_LIMIT) return;
    const part = delta.slice(0, THINKING_LIMIT - this.thinkingChars);
    pre.append(document.createTextNode(part)); this.thinkingChars += part.length;
  }
}
