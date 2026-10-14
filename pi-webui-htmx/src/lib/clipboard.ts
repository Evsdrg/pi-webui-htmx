// 剪贴板助手。
//
// navigator.clipboard 只在**安全上下文**（HTTPS 或 localhost）里存在；本仓的
// 本地部署形态是 http://<ip>:<port> 访问桥，那里它是 undefined。因此不能直接
// 调 writeText，要按能力探测并退回 execCommand，最后如实返回成功与否，
// 让调用方给出可见提示（面板已经有「请手动选择」的文案）。
//
// 返回 true/false：false 表示没能写进剪贴板，调用方**必须**让用户知道，
// 而不是静默失败。

/** copyText 尽力把文本写入剪贴板；返回是否成功。 */
export async function copyText(value: string): Promise<boolean> {
  const clipboard = navigator.clipboard;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(value);
      return true;
    } catch {
      // 权限被拒或非安全上下文：继续走下面的兜底。
    }
  }
  return legacyCopy(value);
}

/** legacyCopy 用可选中文本框 + execCommand 兜底；用户仍可手动复制。 */
export function legacyCopy(value: string): boolean {
  const holder = document.createElement('textarea');
  holder.value = value;
  holder.setAttribute('aria-hidden', 'true');
  holder.style.position = 'fixed';
  holder.style.opacity = '0';
  document.body.append(holder);
  holder.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  holder.remove();
  return ok;
}
