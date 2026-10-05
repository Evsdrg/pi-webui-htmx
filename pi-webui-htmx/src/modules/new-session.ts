// 「新建会话」与目录选择器：低频交互（每次新会话才用一次），
// 独立成按需分块，不占首屏预算（与 branch/models/theme 同一策略）。
//
// 目录选择器的就绪门控（directoryReady）保持在这里：它决定「使用此目录」
// 是否可点，涉及 dir-list 的请求与回显，属于同一块状态。
import { closeDialog, el } from './dom';
import type { Method } from '@/types/protocol';

/** Host 是 Workbench 交给本模块的最小能力面。 */
export interface Host {
  request<T = unknown>(method: Method, params?: unknown, session?: string): Promise<T>;
  selectSession(id: string, cwd: string, title: string): void;
  currentCwd(): string;
  firstDirectory(): boolean;
  selectedModel(): { provider: string; id: string } | undefined;
  applyModelIntent(model: { provider: string; id: string }): void;
  saveDraft(key: string, text: string): void;
  updateControls(): void;
  syncButton(): void;
  fail(error: unknown): void;
}

/** 目录选择器的挂载状态：请求在飞时不可确认。 */
let dirRequest: XMLHttpRequest | undefined;

function directoryReady(): boolean {
  const current = document.getElementById('dir-current') as HTMLInputElement | null;
  if (!current) return true; // 精简嵌入页没有目录浏览器。
  const loaded = document.querySelector<HTMLElement>('#dir-list [data-dir-loaded-path]');
  return !dirRequest && !!current.value && current.value === loaded?.dataset.dirLoadedPath && current.value === el<HTMLInputElement>('cwd-input').value.trim();
}
function updateDirUse(): void {
  const use = document.getElementById('dir-use') as HTMLButtonElement | null;
  if (use) use.disabled = !directoryReady();
}
// syncDirInput 把片段带外交换写进 #dir-current 的当前路径回显到输入框。
function syncDirInput(): void {
  const current = document.getElementById('dir-current') as HTMLInputElement | null;
  const loaded = document.querySelector<HTMLElement>('#dir-list [data-dir-loaded-path]');
  if (current && loaded && current.value === loaded.dataset.dirLoadedPath) el<HTMLInputElement>('cwd-input').value = current.value;
  updateDirUse();
}

// wireDirectory 接目录选择器的请求/回显/「前往」，并返回 openDialog 后的刷新动作。
export function wireDirectory(signal: AbortSignal): void {
  document.body.addEventListener('htmx:afterSwap', (event) => {
    if ((event.target as HTMLElement).id === 'dir-list') syncDirInput();
  }, { signal });
  document.addEventListener('htmx:beforeRequest', (event) => {
    const detail = (event as CustomEvent).detail;
    if (detail?.target?.id === 'dir-list') { dirRequest = detail.xhr; updateDirUse(); }
  }, { signal });
  document.addEventListener('htmx:afterRequest', (event) => {
    if ((event as CustomEvent).detail?.xhr === dirRequest) { dirRequest = undefined; updateDirUse(); }
  }, { signal });
  const dirInput = document.getElementById('cwd-input') as HTMLInputElement | null;
  dirInput?.addEventListener('input', () => {
    const list = document.getElementById('dir-list');
    if (list) list.dataset.requestScope = String(Number(list.dataset.requestScope || '0') + 1);
    updateDirUse();
  }, { signal });
  document.getElementById('dir-go')?.addEventListener('click', () => {
    const path = el<HTMLInputElement>('cwd-input').value.trim();
    if (!path) return;
    const requested = document.getElementById('dir-request') as HTMLInputElement | null;
    if (requested) requested.value = path;
    const use = document.getElementById('dir-use') as HTMLButtonElement | null;
    if (use) use.disabled = true;
    window.htmx.trigger(document.body, 'dirs-refresh');
  }, { signal });
}

/** 打开新建会话对话框：预填当前目录并刷新目录列表。 */
export async function openNewSession(host: Host): Promise<void> {
  const response = await host.request<{ roots: string[] }>('files.roots', undefined, '');
  const start = host.currentCwd() || response.roots[0] || '';
  // 路径输入框先于列表刷新填好：列表用 hx-include 读它，顺序反了会拿到上一次的路径。
  el<HTMLInputElement>('cwd-input').value = start;
  const current = document.getElementById('dir-current') as HTMLInputElement | null;
  if (current) current.value = '';
  const requested = document.getElementById('dir-request') as HTMLInputElement | null;
  if (requested) requested.value = start;
  updateDirUse();
  const dialog = document.getElementById('new-dialog') as HTMLDialogElement | null;
  dialog?.showModal();
  // hx-trigger 上的 load 只在元素首次插入 DOM 时触发，第二次打开不会重新请求。
  window.htmx.trigger(document.body, 'dirs-refresh');
  host.syncButton();
}

/** 提交新建会话对话框：确认目录、切换会话，并把首条草稿/模型意图带过去。 */
export function submitNewSession(host: Host): void {
  const cwd = el<HTMLInputElement>('cwd-input').value.trim();
  if (!cwd || !directoryReady()) return;
  const firstDirectory = host.firstDirectory();
  const input = el<HTMLTextAreaElement>('prompt');
  const draft = firstDirectory ? input.value : '';
  const model = firstDirectory ? host.selectedModel() : undefined;
  host.selectSession('', cwd, '新会话');
  if (firstDirectory) {
    input.value = draft;
    if (model) host.applyModelIntent(model);
    // 目标草稿键是「新会话 + 该目录」：显式写入，不依赖 selectSession 之后
    // 的 this.cwd 时序（那一步会把 prompt 覆盖成该键下已存的草稿）。
host.saveDraft(`new:${cwd}`, draft); host.updateControls();
  }
  closeDialog('new-dialog'); input.focus();
}
