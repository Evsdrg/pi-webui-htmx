// 顶栏面板内部的瞬时交互。
//
// 面板的 HTML 全部由桥渲染（/ui/tools、/ui/stats、/ui/system）。这里只处理
// 两类浏览器侧状态，服务端没有它们的等价物：
//   1. 工具列表的选中项——纯视图状态，切换不发请求（那会重新导出会话快照）；
//   2. 复制按钮——剪贴板只存在于浏览器。
import { copyText } from '@/lib/clipboard';
/** 面板内部点击；返回 true 表示已处理，调用方不再继续分发。 */
export function panelClick(event: Event): boolean {
  const target = event.target as Element | null;
  if (!target) return false;

  const item = target.closest<HTMLElement>('[data-tool-select]');
  if (item) { selectTool(item); return true; }

  const copy = target.closest<HTMLButtonElement>('[data-copy-value]');
  if (copy) { void copyValue(copy); return true; }

  return false;
}

// selectTool 切换工具详情：选中项高亮，其余详情隐藏。
export function selectTool(item: HTMLElement): void {
  const name = item.dataset.toolSelect ?? '';
  for (const button of document.querySelectorAll<HTMLElement>('[data-tool-select]')) {
    const selected = button === item;
    button.classList.toggle('selected', selected);
    button.setAttribute('aria-pressed', String(selected));
  }
  for (const detail of document.querySelectorAll<HTMLElement>('[data-tool-detail]')) {
    detail.hidden = detail.dataset.toolDetail !== name;
  }
}

// copyValue 复制一行事实到剪贴板，并给出短暂的可见反馈（图标换成对勾）。
//
// 剪贴板 API 在非安全上下文或权限被拒时会失败，copyText 内部退回选中文本的
// 兜底并返回是否成功；失败时按钮提示手动复制，而不是静默什么都不做。
async function copyValue(button: HTMLButtonElement): Promise<void> {
  const value = button.dataset.copyValue ?? '';
  if (!value) return;
  const label = button.dataset.copyLabel ?? '内容';
  const ok = await copyText(value);
  // 图标切换交给 CSS（.copy-btn.copied），这里只改 class 与提示文案：
  // 不去改 textContent，避免把模板里的图标抹掉。
  button.classList.toggle('copied', ok);
  button.title = ok ? `已复制${label}` : `复制${label}失败，请手动选择`;
  if (ok) window.setTimeout(() => { button.classList.remove('copied'); button.title = `复制${label}`; }, 1600);
}
