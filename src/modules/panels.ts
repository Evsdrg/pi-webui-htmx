// 顶栏面板内部的瞬时交互。
//
// 面板的 HTML 全部由桥渲染（/ui/tools、/ui/stats、/ui/system）。这里只处理
// 两类浏览器侧状态，服务端没有它们的等价物：
//   1. 工具列表的选中项——纯视图状态，切换不发请求（那会重新导出会话快照）；
//   2. 复制按钮——剪贴板只存在于浏览器。
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

// copyValue 复制一行事实到剪贴板，并给出短暂的可见反馈。
//
// 剪贴板 API 在非安全上下文或权限被拒时会失败，因此退回选中文本的提示，
// 而不是静默什么都不做——用户至少知道要手动复制。
async function copyValue(button: HTMLButtonElement): Promise<void> {
  const value = button.dataset.copyValue ?? '';
  if (!value) return;
  const label = button.dataset.copyLabel ?? '内容';
  let ok = false;
  try {
    if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(value); ok = true; }
  } catch { ok = false; }
  if (!ok) {
    // 没有剪贴板权限时把值放进一个可选中的临时输入框，用户仍能手动复制。
    const holder = document.createElement('textarea');
    holder.value = value; holder.setAttribute('aria-hidden', 'true');
    holder.style.position = 'fixed'; holder.style.opacity = '0';
    document.body.append(holder); holder.select();
    try { ok = document.execCommand('copy'); } catch { ok = false; }
    holder.remove();
  }
  button.classList.toggle('copied', ok);
  button.textContent = ok ? '✓' : '×';
  button.title = ok ? `已复制${label}` : `${label}复制失败，请手动选择`;
  window.setTimeout(() => { button.classList.remove('copied'); button.textContent = '⧉'; button.title = `复制${label}`; }, 1600);
}
