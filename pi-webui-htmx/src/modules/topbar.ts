import { el } from './dom';
import { record, text } from './stream';
import type { Method, ToolPreset, WorkerInfo } from '@/types/protocol';

// TopbarHost 是顶栏功能面板需要的 Workbench 能力。
// 顶栏面板按需加载：首屏不承担这段代码，点击按钮时才 import。
export interface TopbarHost {
  sessionId(): string;
  cwd(): string;
  busy(): boolean;
  modelLabel(): string;
  contextWindow(): number;
  setContextWindow(value: number): void;
  thinking(): string;
  preset(): string;
  setPreset(value: string): void;
  presetKey(): string;
  presetBySession(): Map<string, string>;
  /** setTitle 记录会话名；侧栏列表负责显示，这里只更新内部状态。 */
  setTitle(title: string): void;
  request<T = unknown>(method: Method, params?: unknown, session?: string): Promise<T>;
  notify(message: string, kind?: string): void;
  fail(error: unknown): void;
  refreshState(): Promise<void>;
}

const PANELS = ['panel-info', 'panel-title', 'panel-system', 'panel-tools', 'panel-mc'];

// formatCompact 把 token 数压成短标签，供顶栏上下文用量使用。
function formatCompact(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}k`;
  return String(value);
}

// toggle 打开/关闭顶栏下面的功能面板；同时只保留一个打开，
// 避免三个面板叠在一起把对话区挤掉。target 即面板 id。
export function toggle(host: TopbarHost, target: string, force?: boolean): void {
  const open = force ?? !!document.getElementById(target)?.hidden;
  for (const id of PANELS) {
    const node = document.getElementById(id);
    if (node) node.hidden = id !== target || !open;
    document.querySelector(`[aria-controls="${id}"]`)?.setAttribute('aria-expanded', String(id === target && open));
  }
  if (!open) return;
  if (target === 'panel-info') {
    // 会话详情整体由桥渲染（Pi 的统计数字只存在于活的进程里）。
    el<HTMLInputElement>('stats-session').value = host.sessionId();
    window.htmx.trigger(document.body, 'stats-refresh');
  }
  if (target === 'panel-title') prefillLocalTitle();
  if (target === 'panel-system') {
    // 提示词由桥渲染（它得先让 Pi 导出一份快照）；这里只负责带上会话 ID 触发刷新。
    el<HTMLInputElement>('system-session').value = host.sessionId();
    window.htmx.trigger(document.body, 'system-refresh');
  }
  if (target === 'panel-tools') {
    // 工具面板同理：显示的是本次进程实际暴露给模型的工具，不是预设的自述。
    el<HTMLInputElement>('tools-session').value = host.sessionId();
    window.htmx.trigger(document.body, 'tools-refresh');
  }
  if (target === 'panel-mc') {
    // 记忆面板读本机 magic-context 的 SQLite 库，由桥渲染。
    // 四个隐藏字段各自带自己的 name，由 hx-include 带上去；JS 只改值不拼 URL。
    el<HTMLInputElement>('mc-kind').value = 'memories';
    el<HTMLInputElement>('mc-offset').value = '0';
    el<HTMLInputElement>('mc-category').value = '';
    el<HTMLInputElement>('mc-project').value = '';
    window.htmx.trigger(document.body, 'mc-refresh');
  }
}

// openFullHistory 与 Pi Web 的「完整历史」一致：在新标签页里直接阅读
// 导出的 HTML，不走下载。桥的 /ui/exports 支持 ?inline=1。
export async function openFullHistory(host: TopbarHost): Promise<void> {
  if (!host.sessionId()) { host.notify('会话尚未分配，发送第一条消息后再查看完整历史', 'warning'); return; }
  const result = await host.request<{ path: string }>('session.export_html', { fileName: `session-${host.sessionId().slice(0, 64)}.html` }, host.sessionId());
  const file = (result.path ?? '').split('/').pop();
  if (!file) { host.notify('Pi 没有返回导出文件', 'warning'); return; }
  window.open(`/ui/exports/${encodeURIComponent(file)}?inline=1`, '_blank', 'noopener,noreferrer');
  host.notify('已在新标签页打开完整历史。');
}

function prefillLocalTitle(): void {
  const input = el<HTMLInputElement>('local-title');
  if (input.value.trim()) return;
  input.value = (document.querySelector('#turns .turn-user .bubble')?.textContent ?? '').trim().slice(0, 80);
}

// saveLocalTitle 只做本机摘要标题：Pi Web 用独立的模型调用生成标题，
// 而 pi --mode rpc 没有暴露同类命令，桥不替用户花 token。
export async function saveLocalTitle(host: TopbarHost): Promise<void> {
  const title = el<HTMLInputElement>('local-title').value.trim();
  if (!title) { host.notify('标题不能为空', 'warning'); return; }
  if (!host.sessionId()) { host.notify('会话尚未分配，发送第一条消息后再命名', 'warning'); return; }
  try {
    await host.request('session.set_name', { name: title }, host.sessionId());
    host.setTitle(title);
    host.notify('会话名称已更新。');
    host.refreshState();
  } catch (error) { host.fail(error); }
}

// changeToolPreset 切换工具预设。Pi 的工具集只能在拉起进程时通过
// --tools/--exclude-tools/--no-tools 指定，RPC 没有运行中切换入口，
// 因此改动必须停掉当前 worker 再用新预设启动；运行中的任务会随之中断，
// 这一点必须先向用户确认，不能静默重启。
export async function changeToolPreset(host: TopbarHost): Promise<void> {
  const preset = host.preset() as ToolPreset;
  const key = host.presetKey();
  const previous = host.presetBySession().get(key) ?? '';
  if (preset === (previous || 'default')) return;
  if (host.busy() && !window.confirm('切换工具预设需要重启当前会话的工作进程，进行中的任务会中断。确定继续？')) {
    host.setPreset(previous || 'default');
    return;
  }
  try {
    if (host.sessionId()) await host.request('session.stop', { force: false }, host.sessionId()).catch(() => {});
    host.presetBySession().set(key, preset);
    const info = await host.request<WorkerInfo>('session.start', { cwd: host.cwd(), toolPreset: preset }, host.sessionId());
    if (info.sessionId && info.sessionId !== host.sessionId()) host.presetBySession().set(info.sessionId, preset);
    host.notify('工具预设已更新；新预设在下一次启动工作进程时生效。');
    host.setPreset(preset);
    await host.refreshState();
  } catch (error) {
    host.presetBySession().set(key, previous);
    host.setPreset(previous || 'default');
    throw error;
  }
}

// renderContextUsage 显示 Pi 的 get_session_stats.contextUsage。
// 这个字段只有 worker 持有带 contextWindow 的模型时才非空；压缩后
// 尚未产生新的助手回复时 Pi 会返回 percent/tokens 为 null，界面必须
// 显示「未知」而不是编一个 0。magic-context 另有一条自己的 mc: 状态行，
// 两者数据来源不同，不做合并。
export function renderContextUsage(host: TopbarHost, usage: unknown): void {
  const node = el('context-usage');
  const value = (usage && typeof usage === 'object') ? usage as { tokens: number | null; contextWindow: number; percent: number | null } : undefined;
  if (!value || typeof value.contextWindow !== 'number' || value.contextWindow <= 0) { node.hidden = true; node.textContent = ''; node.title = ''; return; }
  const percent = typeof value.percent === 'number' ? value.percent : null;
  const tokens = typeof value.tokens === 'number' ? value.tokens : null;
  const share = percent === null ? '?' : `${Math.round(percent)}%`;
  const used = tokens === null ? '' : ` · ${formatCompact(tokens)}`;
  node.textContent = `${share}${used} / ${formatCompact(value.contextWindow)}`;
  node.title = percent === null || tokens === null
    ? `上下文 ${share} / ${value.contextWindow.toLocaleString()} tokens（压缩后或统计不可用，待下次回复确认）`
    : `上下文 ${percent.toFixed(1)}% · ${tokens.toLocaleString()} / ${value.contextWindow.toLocaleString()} tokens`;
  node.hidden = false;
  host.setContextWindow(value.contextWindow);
}

// renderUsage 填充底栏用量条，并同步顶栏上下文占比。
//
// Pi 0.85.1 的 get_session_stats 在会话含失败回合时会整体失败；这时用量条留空，
// 但上下文窗口仍来自 session.state 的模型元数据，必须继续显示，并说明占比未知。
export async function renderUsage(host: TopbarHost, sessionId: string): Promise<void> {
  let stats: Record<string, unknown> = {};
  try { stats = record(await host.request('session.stats', undefined, sessionId)); } catch { stats = {}; }
  if (host.sessionId() !== sessionId) return;
  el('usage').textContent = [typeof stats.totalMessages === 'number' ? `${stats.totalMessages} 条消息` : '', typeof stats.cost === 'number' ? `$${Number(stats.cost).toFixed(4)}` : ''].filter(Boolean).join(' · ');
  renderContextUsage(host, stats.contextUsage ?? (host.contextWindow() > 0 ? { tokens: null, contextWindow: host.contextWindow(), percent: null } : undefined));
}

export const topbarText = text;
