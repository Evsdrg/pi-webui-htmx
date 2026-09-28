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
  request<T = unknown>(method: Method, params?: unknown, session?: string): Promise<T>;
  notify(message: string, kind?: string): void;
  fail(error: unknown): void;
  refreshState(): Promise<void>;
}

const PANELS = ['panel-title', 'panel-system', 'panel-tools'];

// formatCompact 把 token 数压成短标签，供顶栏上下文用量使用。
function formatCompact(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}K`;
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
  if (target === 'panel-title') prefillLocalTitle();
  if (target === 'panel-system') renderSystemFacts(host);
  if (target === 'panel-tools') renderToolPresetNote(host);
}

function renderSystemFacts(host: TopbarHost): void {
  const rows: [string, string][] = [
    ['模型', host.modelLabel()],
    ['上下文窗口', host.contextWindow() > 0 ? `${host.contextWindow().toLocaleString()} tokens` : '未知'],
    ['工作目录', host.cwd() || '未选择'],
    ['会话 ID', host.sessionId() || '尚未分配'],
    ['思考强度', host.thinking() || '自动'],
    ['工具预设', host.preset()],
  ];
  el('system-facts').replaceChildren(...rows.flatMap(([label, value]) => {
    const term = document.createElement('dt'); term.textContent = label;
    const desc = document.createElement('dd'); desc.textContent = value;
    return [term, desc];
  }));
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
    el('session-title').textContent = title;
    host.notify('会话名称已更新。');
    host.refreshState();
  } catch (error) { host.fail(error); }
}

function renderToolPresetNote(host: TopbarHost): void {
  const notes: Record<string, string> = {
    'chat-only': '不启用任何工具（内置与扩展），只做纯对话。',
    'read-only': '保留 read/grep/find/ls 与扩展工具，禁用 bash/edit/write。',
    default: 'Pi 的默认工具集 read/bash/edit/write，扩展工具保持可用。',
    full: '启用内置 7 项工具。Pi 的 --tools 是白名单，会连带禁用扩展工具（含 magic-context），这是上游限制。',
  };
  el('tool-preset-note').textContent = notes[host.preset()] ?? '';
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
  renderToolPresetNote(host);
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
  el('usage').textContent = [typeof stats.totalMessages === 'number' ? `${stats.totalMessages} 条消息` : '', typeof stats.cost === 'number' ? `$${Number(stats.cost).toFixed(4)}` : ''].filter(Boolean).join(' · ');
  renderContextUsage(host, stats.contextUsage ?? (host.contextWindow() > 0 ? { tokens: null, contextWindow: host.contextWindow(), percent: null } : undefined));
}

export const topbarText = text;
