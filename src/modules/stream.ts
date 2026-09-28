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
  accept(epoch: string, seq: number): boolean {
    if (!epoch || !Number.isSafeInteger(seq) || seq <= 0) return false;
    if (epoch !== this.epoch) { this.epoch = epoch; this.seq = 0; }
    if (seq <= this.seq) return false;
    this.seq = seq; return true;
  }
  reset(): void { this.epoch = ''; this.seq = 0; }
}
const MAX_LIVE_CHARS = 200_000;

// THINKING_LIMIT 限制思考块的实时字符数，超出部分不再追加。
const THINKING_LIMIT = 40_000;

export class LiveView {
  private frame = 0;
  private chunks = '';
  private rendered = 0;
  private toolCount = 0;
  private truncated = false;
  /** thinkingChars 记录已写入的思考字符数，用于限额而不必读 DOM。 */
  private thinkingChars = 0;
  constructor(private readonly root: HTMLElement) {}
  begin(userText?: string): void {
    this.clear(); this.thinkingChars = 0; this.root.hidden = false; this.root.dataset.running = 'true';
    const user = this.root.querySelector<HTMLElement>('#live-user');
    if (user) { user.textContent = userText ?? ''; user.hidden = !userText; }
  }
  event(event: Record<string, unknown>): void {
    if (event.type === 'message_start') { this.flush(); this.append('\n\n'); }
    if (event.type === 'message_update') {
      const delta = record(event.assistantMessageEvent);
      if (delta.type === 'text_delta') this.append(text(delta.delta));
      if (delta.type === 'thinking_delta') this.appendThinking(text(delta.delta));
    }
    if (event.type === 'tool_execution_start') {
      this.root.hidden = false;
      const tools = this.root.querySelector('#live-tools');
      if (tools && this.toolCount++ < 100) { const line = document.createElement('div'); line.textContent = `调用 ${text(event.toolName) || '工具'}`; tools.append(line); }
    }
    if (event.type === 'message_end') {
      const message = record(event.message);
      if (message.stopReason === 'error') this.append('\n[模型请求失败；完成后查看历史中的错误提示]');
    }
  }
  finish(): void { this.flush(); delete this.root.dataset.running; }
  clear(): void {
    cancelAnimationFrame(this.frame); this.frame = 0; this.chunks = ''; this.rendered = 0; this.toolCount = 0; this.truncated = false; this.thinkingChars = 0;
    for (const id of ['live-text','live-thinking','live-tools','live-user']) this.root.querySelector(`#${id}`)?.replaceChildren();
    this.root.hidden = true; delete this.root.dataset.running;
  }
  dispose(): void { cancelAnimationFrame(this.frame); this.chunks = ''; }
  private append(delta: string): void {
    this.root.hidden = false;
    const remaining = MAX_LIVE_CHARS - this.rendered - this.chunks.length;
    if (delta.length > remaining && !this.truncated) {
      this.chunks += delta.slice(0, Math.max(0, remaining)) + '\n[实时预览已达上限，完成后读取持久历史]'; this.truncated = true;
    } else if (!this.truncated) this.chunks += delta;
    if (!this.frame) this.frame = requestAnimationFrame(() => this.flush());
  }
  private flush(): void {
    cancelAnimationFrame(this.frame); this.frame = 0;
    const el = this.root.querySelector('#live-text');
    if (el && this.chunks) {
      let node = el.firstChild;
      if (!(node instanceof Text)) { node = document.createTextNode(''); el.replaceChildren(node); }
      (node as Text).appendData(this.chunks); this.rendered += this.chunks.length; this.chunks = '';
    }
  }
  /**
   * appendThinking 追加思考增量。
   *
   * 必须用 appendChild 增量追加，不能「读整段 + 拼 + 写回」：
   * 后者每个 token 都要复制最多 40K 字符，长推理下是重复的整段拷贝，
   * 前端 CPU 与 GC 都会被放大（U21）。追加文本节点是 O(增量)。
   */
  private appendThinking(delta: string): void {
    const el = this.root.querySelector('#live-thinking');
    if (!el) return;
    const used = this.thinkingChars;
    if (used >= THINKING_LIMIT) return;
    const room = THINKING_LIMIT - used;
    el.append(document.createTextNode(delta.slice(0, room)));
    this.thinkingChars = used + Math.min(delta.length, room);
  }
}
