// 扩展通知（notify）的呈现。
//
// Pi 的 RPC 模式把 notify 作为 fire-and-forget 的 extension_ui_request
// 推来，不带回执。前端渲染成 toast，几秒后自动消失。
//
// 约束：
//   - 内容来自插件，一律按不可信文本处理（textContent，不用 innerHTML）
//   - 同时存在的 toast 有上限，超出丢弃最旧的，防止插件刷屏
//   - 不阻塞、不抢焦点

const MAX_VISIBLE = 4;
const DEFAULT_TTL_MS = 6000;

export type NotifyKind = "info" | "success" | "warning" | "error";

let container: HTMLElement | null = null;

function ensureContainer(): HTMLElement {
  if (container?.isConnected) return container;
  container = document.createElement("div");
  container.id = "toast-shelf";
  container.setAttribute("role", "status");
  container.setAttribute("aria-live", "polite");
  document.body.appendChild(container);
  return container;
}

/** 弹出一条通知。message 必须是纯文本。 */
export function showToast(message: string, kind: NotifyKind = "info", ttlMs = DEFAULT_TTL_MS): void {
  const text = (message ?? "").trim();
  if (!text) return;
  const shelf = ensureContainer();

  // 超量时先撤掉最旧的一条。
  while (shelf.children.length >= MAX_VISIBLE) {
    shelf.firstElementChild?.remove();
  }

  const el = document.createElement("div");
  el.className = `toast toast-${kind}`;
  // 只用 textContent：插件内容不可信，不能走 innerHTML。
  el.textContent = text.length > 500 ? `${text.slice(0, 500)}…` : text;

  const close = document.createElement("button");
  close.type = "button";
  close.className = "toast-close";
  close.setAttribute("aria-label", "关闭");
  close.textContent = "×";
  close.addEventListener("click", () => el.remove());
  el.appendChild(close);

  shelf.appendChild(el);
  if (ttlMs > 0) {
    window.setTimeout(() => el.remove(), ttlMs);
  }
}
