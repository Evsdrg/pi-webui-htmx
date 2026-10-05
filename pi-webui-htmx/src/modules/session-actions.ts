// 低频会话结构操作（「从此处编辑」会话内跳转、删除、重命名）的落点逻辑。
// 放独立分块按需加载（与 branch/models 同一策略）：都带解释性文案、
// 又不是每次使用，不值得占用首屏预算。
import { closeDialog, el } from './dom';
import { absoluteUrl } from '../lib/url';
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
  notify(message: string): void;
  sessionId: string;
  cwd: string;
  /** 行内重命名的新名称（仅 rename 使用）。 */
  name?: string;
}

export async function run(kind: string, host: Host, button: HTMLElement): Promise<void> {
  if (kind === 'delete') return deleteSession(host);
  if (kind === 'rename') return renameSession(host);
  if (kind === 'clone') return cloneSession(host);
  if (kind === 'export') return exportSession(host);
  return editHere(host, button);
}

// cloneSession 复制当前会话为一条新会话（身份变更）。
async function cloneSession(host: Host): Promise<void> {
  try {
    const result = (await host.command('session.clone')) as { sessionId?: unknown };
    if (typeof result.sessionId === 'string') {
      host.selectSession(result.sessionId, host.cwd, '克隆会话');
      host.refreshSessions();
    }
  } catch (error) { host.fail(error); }
}

// exportSession 导出为磁盘 HTML 并触发下载。
async function exportSession(host: Host): Promise<void> {
  try {
    // 用完整会话 ID，截断只会得到没有辨识度的名字。
    const name = `session-${host.sessionId.slice(0, 64)}.html`;
    // 导出是磁盘投影：刻意不走 command()（它会先 ensureWorker），
    // 只看历史不该拉起 Pi 进程（B76）。显式绑定当前会话，不按空串走隐含默认。
    const result = (await host.request('session.export_html', { fileName: name }, host.sessionId)) as { path?: unknown };
    const file = (typeof result.path === 'string' ? result.path.split('/').pop() : '') || name;
    host.notify('已导出，开始下载。');
    // 走普通导航而不是 fetch：需要浏览器弹出下载，且要带登录 Cookie。
    // 用文档基地址解析：`location.assign` 不受 <base href> 影响，
    // 写根绝对路径在设备前缀形态下会跳出前缀，下载 404（B54）。
    location.assign(absoluteUrl(`ui/exports/${encodeURIComponent(file)}`).toString());
  } catch (error) { host.fail(error); }
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

// renameSession 给任意会话改名。Pi 的 set_name 依赖活动工作进程，
// 所以目标不是当前会话时先临时拉起一个（按行上的 cwd），改完立即释放，
// 不让它白占一个进程直到空闲回收。
async function renameSession(host: Host): Promise<void> {
  const target = host.sessionId;
  const name = (host.name ?? '').trim();
  if (!target || !name) return;
  try {
    const owned = await startIfNeeded(host, target);
    await host.request('session.set_name', { name }, target);
    if (owned) await host.request('session.stop', { force: false }, target).catch(() => {});
    host.refreshSessions();
  } catch (error) { host.fail(error); }
}

// deleteSession 删除会话的磁盘记录。忙会话必须能删：force 让桥先强制
// 停掉工作进程再删文件；不带 force 的旧路径在会话运行中永远删不掉（B08）。
async function deleteSession(host: Host): Promise<void> {
  const target = host.sessionId;
  if (!target) return;
  if (!confirm('删除此会话的磁盘记录？运行中的工作进程会被强制停止。此操作无法撤销。')) return;
  try {
    await host.request('sessions.delete', { sessionId: target, force: true }, '');
    // 被删的是当前会话时清空选中；行内删除其他会话时不动当前视图。
    if (target === el('workbench')?.dataset.currentSession || document.body.dataset.sessionId === target) {
      el('edit-scope').hidden = true;
      host.selectSession('', host.cwd, '新会话');
    }
    host.refreshSessions();
    closeDialog('session-dialog');
  } catch (error) { host.fail(error); }
}

// startIfNeeded 确保目标会话有活动工作进程，返回「是否由本次调用拉起」。
// 改名依赖 Pi 的 set_name（需要进程）；已运行的会话直接复用，不重复启动。
async function startIfNeeded(host: Host, target: string): Promise<boolean> {
  const workers = (await host.request('worker.list')) as unknown;
  const list = Array.isArray(workers) ? workers : [];
  const alive = list.some((w) => (w as { sessionId?: string })?.sessionId === target);
  if (alive) return false;
  const row = document.querySelector<HTMLElement>(`[data-session="${CSS.escape(target)}"]`);
  const cwd = row?.dataset.cwd ?? host.cwd;
  await host.request('session.start', { cwd }, target);
  return true;
}
