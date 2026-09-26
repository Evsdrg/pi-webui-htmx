// 共用的 DOM 辅助。
//
// el() 曾在五个模块里各写一遍——完全相同的单行函数。
// 抽到这里不是为了少几行，而是为了让「取不到元素时怎么办」
// 只有一种答案：下面那个 elOrNull。
//
// 两者分工：
//   el       调用方确定元素存在（模板里写死了 id）
//   elOrNull 元素可能不存在（例如不同 UI 版本、或可选面板）

export function el<T extends HTMLElement = HTMLElement>(id: string): T {
  return document.getElementById(id) as T;
}

export function elOrNull<T extends HTMLElement = HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

// openDialog 打开一个模态对话框。
// 四五个调用点各写一遍 el<HTMLDialogElement>('x').showModal()，
// 而且 showLogin/new-dialog 还都带了 open 判断、action 里那几个没有——
// 重复之下判断条件不一致，正是这类代码容易长出 bug 的地方。
export function openDialog(id: string): HTMLDialogElement {
  const dialog = document.getElementById(id) as HTMLDialogElement | null;
  if (dialog && !dialog.open) dialog.showModal();
  return dialog as HTMLDialogElement;
}

export function closeDialog(id: string): void {
  const dialog = document.getElementById(id) as HTMLDialogElement | null;
  if (dialog?.open) dialog.close();
}
