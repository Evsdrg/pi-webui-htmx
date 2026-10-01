// 浏览器只保存有界的在途请求和传输状态，不复制完整会话。
import type { Message, Method, Request } from '@/types/protocol';

export class BridgeError extends Error {
  constructor(public readonly code: string, message: string) { super(message); this.name = 'BridgeError'; }
}
type Pending = { resolve(value: unknown): void; reject(reason: unknown): void; timer: ReturnType<typeof setTimeout> };
export type SocketFactory = (url: string) => WebSocket;

export function decodeMessage(raw: unknown): Message | null {
  if (typeof raw !== 'string' || raw.length > 2 * 1024 * 1024) return null;
  try {
    const value = JSON.parse(raw) as Record<string, unknown> | null;
    if (!value || value.version !== 1) return null;
    if (value.kind === 'response' && typeof value.requestId === 'string' && typeof value.ok === 'boolean') return value as unknown as Message;
    if ((value.kind === 'event' || value.kind === 'control') && typeof value.event === 'string') return value as unknown as Message;
  } catch { /* 无效帧不进入业务层。 */ }
  return null;
}

export class BridgeClient extends EventTarget {
  private socket: WebSocket | null = null;
  private pending = new Map<string, Pending>();
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private backoff = 500;
  private disposed = false;
  private sequence = 0;
  private readonly prefix = crypto.randomUUID();
  private sent = 0;

  constructor(private readonly url: string, private readonly factory: SocketFactory = (url) => new WebSocket(url)) { super(); }
  get connected(): boolean { return this.socket?.readyState === 1; }
  get pendingCount(): number { return this.pending.size; }

  connect(): void {
    if (this.disposed || this.socket) return;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    let socket: WebSocket;
    try { socket = this.factory(this.url); } catch { this.scheduleReconnect(); return; }
    this.socket = socket;
    socket.onopen = () => {
      if (this.socket !== socket || this.disposed) return;
      this.sent = 0; this.backoff = 500;
      this.dispatchEvent(new Event('connected'));
    };
    socket.onmessage = (event) => {
      if (this.socket !== socket || this.disposed) return;
      const message = decodeMessage(event.data);
      if (!message) return;
      if (message.kind === 'response') {
        const waiter = this.pending.get(message.requestId);
        if (!waiter) return;
        clearTimeout(waiter.timer); this.pending.delete(message.requestId);
        if (message.ok) waiter.resolve(message.data);
        else waiter.reject(new BridgeError(message.error?.code ?? 'internal', message.error?.message ?? '桥返回了错误'));
      } else this.dispatchEvent(new CustomEvent('message', { detail: message }));
    };
    socket.onclose = () => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.rejectPending(new BridgeError('outcome_unknown', '连接已断开，命令执行结果未知；请核对状态后再操作。'));
      this.dispatchEvent(new Event('disconnected'));
      this.scheduleReconnect();
    };
    socket.onerror = () => { if (this.socket === socket) socket.close(); };
  }

  request<T = unknown>(method: Method, sessionId = '', params?: unknown, timeout = 30_000): Promise<T> {
    const socket = this.socket;
    if (this.disposed || !socket || socket.readyState !== 1) return Promise.reject(new BridgeError('disconnected', '尚未连接到桥，消息未发送。'));
    if (this.pending.size >= 16 || socket.bufferedAmount > 512 * 1024) return Promise.reject(new BridgeError('limit_exceeded', '连接繁忙，请稍后重试；请求未发送。'));
    // 桥每连接保留有限个回执；主动轮换，避免耗尽后所有命令被拒绝。
    if (this.sent >= 900 && this.pending.size === 0) { socket.close(); return Promise.reject(new BridgeError('disconnected', '正在更新连接，请稍后重试；请求未发送。')); }
    const requestId = `${this.prefix}-${++this.sequence}`;
    const request: Request = { version: 1, kind: 'command', requestId, method, ...(sessionId ? { sessionId } : {}), ...(params === undefined ? {} : { params }) };
    let payload: string;
    try { payload = JSON.stringify(request); } catch { return Promise.reject(new BridgeError('invalid_params', '请求内容无法编码')); }
    if (new TextEncoder().encode(payload).length > 1024 * 1024) return Promise.reject(new BridgeError('limit_exceeded', '消息超过桥的单帧上限'));
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(requestId);
        reject(new BridgeError('outcome_unknown', '等待回执超时，执行结果未知；不会自动重发。'));
      }, timeout);
      this.pending.set(requestId, { resolve: (value) => resolve(value as T), reject, timer });
      try { socket.send(payload); this.sent++; } catch {
        clearTimeout(timer); this.pending.delete(requestId);
        reject(new BridgeError('outcome_unknown', '发送中连接中断，请先核对执行状态。'));
      }
    });
  }

  dispose(): void {
    this.disposed = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    const socket = this.socket; this.socket = null;
    if (socket) { socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null; socket.close(); }
    this.rejectPending(new BridgeError('disconnected', '连接已关闭'));
  }
  private rejectPending(error: BridgeError): void {
    for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(error); }
    this.pending.clear();
  }
  private scheduleReconnect(): void {
    if (this.disposed || this.reconnectTimer) return;
    this.reconnectTimer = setTimeout(() => { this.reconnectTimer = undefined; this.connect(); }, this.backoff);
    this.backoff = Math.min(this.backoff * 2, 10_000);
  }
}
