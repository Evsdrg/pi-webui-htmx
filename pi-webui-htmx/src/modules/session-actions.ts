// 低频会话结构操作（「从此处编辑」会话内跳转、删除会话）的落点逻辑。
// 放独立分块按需加载（与 branch/models 同一策略）：两者都带一段解释性
// 文案、又不是每次使用，不值得占用首屏预算。
import { closeDialog, el } from './dom';
import type { Method } from '@/types/protocol';

/** Host 是 Workbench 交给本模块的最小能力面。 */
export interface Host {
  /** command 自动拉起 worker；request 固定会话（session 为空串表示不依赖 worker）。 */
  command(method: Method, params?: unknown): Promise<unknown>;
  request(method: Method, params?: unknown, session?: string): Promise<unknown>;
  refreshHistory(leafId?: string): Promise<void>;
  selectSession(id: string, cwd: string, title: string): void;
  refreshSessions(): void;
  fail(error: unknown): void;
  sessionId: string;
  cwd: string;
}

export async function run(kind: string, host: Host, button: HTMLElement): Promise<void> {
  if (kind === 'delete') return deleteSession(host);
  return editHere(host, button);
}

// editHere 是「从此处编辑」：会话内跳转（session.navigate），不新建会话。
// 叶子移回所选用户消息的父节点，视图截断到该位置，原文回填输入框；
// 之后发送的新消息从该位置分叉，原分支完整保留在同一个会话文件里。
// 运行中的拒绝由桥裁决（忙会计入 waitingInput/pending，前端 run 状态可能落后）。
async function editHere(host: Host, button: HTMLElement): Promise<void> {
  const entryId = button.dataset.entryId;
  if (!entryId) return;
  try {
    const result = (await host.command('session.navigate', { entryId })) as { leafId?: unknown };
    const leafId = typeof result.leafId === 'string' ? result.leafId : '';
    // 回填的原文不能直接读气泡 textContent：带了图片的回合气泡里还挂着
    // 「查看附带图片」按钮，那串字会混进原文——复制节点、摘掉图片入口再取。
    const bubble = button.closest('.turn-user')?.querySelector('.bubble');
    const clone = bubble?.cloneNode(true) as HTMLElement | undefined;
    clone?.querySelectorAll('.user-image-entry').forEach((node) => node.remove());
    const input = el<HTMLTextAreaElement>('prompt');
    input.value = clone?.textContent?.trim() ?? '';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.focus();
    el('edit-scope').hidden = false;
    // 跳转后的分支视图：叶子之前的回合留着，之后的截掉；根叶子则清空。
    if (leafId) await host.refreshHistory(leafId);
    else el('turns').replaceChildren();
    // 上面那次刷新带了 leafId 参数，afterSwap 会顺手打开历史查看条；
    // 编辑态下截断视图就是当前分支，这条会误导，落定后再关掉。
    el('history-scope').hidden = true;
  } catch (error) { host.fail(error); }
}

// deleteSession 删除会话的磁盘记录。忙会话必须能删：force 让桥先强制
// 停掉工作进程再删文件；不带 force 的旧路径在会话运行中永远删不掉（B08）。
async function deleteSession(host: Host): Promise<void> {
  if (!host.sessionId) return;
  if (!confirm('删除此会话的磁盘记录？运行中的工作进程会被强制停止。此操作无法撤销。')) return;
  try {
    await host.request('sessions.delete', { sessionId: host.sessionId, force: true }, '');
    // 编辑横幅属于被删会话的叶子位置，随删除一起收掉。
    el('edit-scope').hidden = true;
    host.selectSession('', host.cwd, '新会话');
    host.refreshSessions();
    closeDialog('session-dialog');
  } catch (error) { host.fail(error); }
}
