// 滚动位置管理。
//
// 为什么不用 Pi Web 那套「保持离底部距离」的公式：
// 那个公式只在新增高度全部位于视口上方时正确。Pi Web 按单条消息切片，
// 翻页时会把已在屏幕上的 assistant 重新折进 ProcessDetailsGroup，
// 新增高度有一部分在当前轮内部，于是底部被钉死、中间被撑开。
//
// 我们的做法分两层：
//
// 1. 结构性：一个历史片段只含完整回合，插入位置永远在轮边界（服务端保证）
// 2. 行为层：由「用户当时是否贴在底部」决定，而不是由桥单方下模式
//
// 第 2 点是关键。桥的 X-Scroll-Mode 只能猜，它看不到浏览器状态：
//   - 用户正在读历史时，append 模式会把他强行拉到底部
//   - 用户贴着底部等流式输出时，prepend 模式会让新内容把他顶离底部
// 两者都由前端判断才准确。桥的头降级为提示，不再作为唯一依据。

const SCROLL_MODE_HEADER = "X-Scroll-Mode";

/** 判定「贴在底部」的像素阈值。容忍亚像素、懒加载图片与字体替换。 */
const BOTTOM_SLOP = 80;

/** 记录某次交换前的离底部距离。 */
function captureDistance(el: HTMLElement): number {
  return el.scrollHeight - el.scrollTop;
}

/** 恢复离底部距离。只在新增内容全部位于视口上方时使用。 */
function restoreDistance(el: HTMLElement, distance: number): void {
  el.scrollTop = Math.max(0, el.scrollHeight - distance);
}

/** 是否贴在底部。 */
export function isAtBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < BOTTOM_SLOP;
}

/** 找到承载回合的滚动容器。 */
function turnsContainer(): HTMLElement | null {
  return document.getElementById("turns");
}

/**
 * 挂载滚动处理。
 *
 * beforeSwap：记录用户是否贴在底部，以及当时的离底部距离。
 * afterSwap：按记录决定——
 *   - 贴在底部 → 滚到新底部（流式输出、切会话）
 *   - 不在底部 → 保持离底部距离（向上翻页）
 *
 * 目标元素可能是 #turns（首屏/切会话）或翻页哨兵（向上翻页），
 * 两种情况都归并到同一个容器上处理。
 */
export function mountScroll(): void {
  const state = new WeakMap<Element, { atBottom: boolean; distance: number }>();

  document.body.addEventListener("htmx:beforeSwap", (event: Event) => {
    const detail = (event as CustomEvent).detail as { target?: Element };
    const target = detail.target;
    if (!(target instanceof HTMLElement)) return;
    const container = turnsContainer();
    if (!container) return;
    // 空容器（首次加载）没有「用户意图」可言。此时记录 atBottom=true，
    // 让首屏落在最新消息上——这与「打开一个会话想看最新进展」一致。
    // 若用户已经翻过历史，容器非空，isAtBottom 会给出真实答案。
    const hadContent = container.querySelectorAll(".turn").length > 0;
    state.set(target, {
      atBottom: hadContent ? isAtBottom(container) : true,
      distance: captureDistance(container),
    });
  });

  document.body.addEventListener("htmx:afterSwap", (event: Event) => {
    const detail = (event as CustomEvent).detail as { target?: Element };
    const target = detail.target;
    if (!(target instanceof HTMLElement)) return;
    const saved = state.get(target);
    if (!saved) return;
    state.delete(target);
    const container = turnsContainer();
    if (!container) return;
    if (saved.atBottom) {
      container.scrollTop = container.scrollHeight;
    } else {
      restoreDistance(container, saved.distance);
    }
  });

  // 窗口尺寸变化时，若仍贴着底部就继续贴住。
  window.addEventListener("resize", () => {
    const container = turnsContainer();
    if (container && isAtBottom(container)) {
      container.scrollTop = container.scrollHeight;
    }
  });
}

/** 读取桥下发的滚动模式。仅作诊断记录，不参与决策。 */
export function scrollModeFrom(detail: { xhr?: XMLHttpRequest }): string {
  return detail.xhr?.getResponseHeader(SCROLL_MODE_HEADER) ?? "append";
}
