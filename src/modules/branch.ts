// 分支导航。
//
// 分支树和可分支消息列表都由桥渲染（`/ui/branch`），这里只做三件属于
// 浏览器的事：带上会话 ID 触发片段刷新、把点击翻译成动作、把失败写回
// 状态行。树的结构、缩进、分叉计数、当前叶子标记都是服务端数据，
// 在前端用 createElement 重搭一遍等于把后端的排版搬进浏览器。
import type { BridgeClient } from './bridge';
import { el } from './dom';

export class BranchNavigator {
  /** 本轮片段请求是否由本模块发起；用于把错误归到分支面板。 */
  private pending = false;

  constructor(
    private readonly bridge: BridgeClient,
    private readonly onError: (error: unknown) => void,
    private readonly goto: (leafId: string) => void,
    private readonly fork: (entryId: string) => void,
    // sessionId 由调用方提供：桥要求 session.* 命令带会话 ID，
    // 传空串会被当成「未启动」拒绝。用函数而不是值，避免会话切换后失效。
    private readonly sessionId: () => string,
    signal?: AbortSignal,
  ) {
    // 片段由桥渲染，错误与完成都通过 htmx 事件回到这里，
    // 因此不需要持有任何 DOM 引用。
    document.addEventListener('htmx:afterSwap', (event) => {
      if (!this.pending) return;
      if ((event as CustomEvent).detail?.target?.id !== 'branch-body') return;
      this.pending = false;
      el('branch-status').textContent = '';
    }, { signal });
    document.addEventListener('htmx:responseError', (event) => {
      if (!this.pending) return;
      const xhr = (event as CustomEvent).detail?.xhr as XMLHttpRequest | undefined;
      if (!xhr?.responseURL?.includes('/ui/branch')) return;
      this.pending = false;
      this.onError(new Error(branchErrorText(xhr)));
      el('branch-status').textContent = branchErrorText(xhr);
    }, { signal });
  }

  async open(): Promise<void> {
    el('branch-status').textContent = '';
    await this.refresh();
  }

  // refresh 触发片段重取。叶子高亮由桥按请求里的 leafId 决定，
  // 「正在读取」提示一直留到片段真正落地或请求失败。
  async refresh(): Promise<void> {
    el('branch-status').textContent = '正在读取会话树…';
    el<HTMLInputElement>('branch-session').value = this.sessionId();
    this.pending = true;
    window.htmx.trigger(document.body, 'branch-refresh');
  }

  // handleClick 处理分支面板里的两个动作；返回是否已处理。
  handleClick(event: Event): boolean {
    const target = (event.target as Element)?.closest<HTMLElement>('[data-branch-goto],[data-branch-fork]');
    if (!target) return false;
    event.preventDefault();
    const leafId = target.dataset.branchGoto;
    if (leafId) { this.goto(leafId); return true; }
    const entryId = target.dataset.branchFork;
    if (entryId) this.fork(entryId);
    return true;
  }

  /** 片段由桥渲染，本模块没有需要手动释放的 DOM 或定时器。 */
  dispose(): void { void this.bridge; }
}

// branchErrorText 取出桥给出的可读原因；取不到就退回状态码。
function branchErrorText(xhr: XMLHttpRequest): string {
  try {
    const body = JSON.parse(xhr.responseText) as { error?: { message?: string } };
    if (body.error?.message) return body.error.message;
  } catch { /* 保留状态码 */ }
  return `读取会话树失败（${xhr.status}）`;
}
