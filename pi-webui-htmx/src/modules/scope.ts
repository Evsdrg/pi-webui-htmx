/**
 * 会话作用域：给每个跨会话的异步操作一个不可伪造的归属标识。
 *
 * 为什么需要它：前端有大量「先 await 再读 this.sessionId」的代码，
 * 等待期间用户切到别的会话，操作就会落到新会话上（U03/U13）。
 * 也有一批「响应回来后直接写 DOM」的路径，旧会话的迟到响应
 * 会覆盖新会话的内容（U06/U09/U10/U17/U18）。
 *
 * 用法：
 *   const scope = this.scope.current();   // 发起时取一次
 *   ...await 任意长时间...
 *   if (!scope.alive()) return;           // 回来后校验
 *   scope.write(...)                      // 只有仍然有效才执行
 *
 * 刻意不做「按 URL 判断」：URL 只在 pushState 时变化，
 * A→B→A 会让两个不同代次共用同一个 URL，守卫会失效。
 */

/** Scope 是一次异步操作的归属凭证。 */
export interface Scope {
  /** 发起时归属的会话 ID。 */
  readonly sessionId: string;
  /** 发起时的代次；切会话会递增。 */
  readonly generation: number;
  /** 操作归属的会话是否仍是当前会话。 */
  alive(): boolean;
  /** alive 为真时执行，否则直接丢弃。用于「回来后写 DOM」。 */
  write<T>(fn: () => T): T | undefined;
}

/** ScopeOwner 持有当前代次，并在切换会话时递增。 */
export class SessionScope {
  private sessionId: string;
  private generation = 0;

  constructor(initialSessionId = '') {
    this.sessionId = initialSessionId;
  }

  /** 当前会话 ID。 */
  get current(): string {
    return this.sessionId;
  }

  /** 当前代次。切会话时递增，只增不减。 */
  get epoch(): number {
    return this.generation;
  }

  /** 取一个归属当前会话的凭证。异步操作发起时必须调用一次。 */
  current$(): Scope {
    const sessionId = this.sessionId;
    const generation = this.generation;
    return {
      sessionId,
      generation,
      alive: () => this.sessionId === sessionId && this.generation === generation,
      write: <T,>(fn: () => T): T | undefined => {
        if (this.sessionId !== sessionId || this.generation !== generation) return undefined;
        return fn();
      },
    };
  }

  /**
   * 切换到新会话：递增代次，让所有在途凭证立即失效。
   * 返回新代次，供调用方作为本地 generation 使用。
   */
  switchTo(sessionId: string): number {
    this.sessionId = sessionId;
    this.generation += 1;
    return this.generation;
  }
}
