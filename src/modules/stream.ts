// 流式层：htmx 负责请求-响应界面，这里只补它不擅长的增量渲染。
//
// 三条硬约束：
// 1. 只做 textContent 追加，绝不重解析已有节点——逐 token 重渲染会丢光标、闪烁。
// 2. agent_settled 后整轮重取，拿权威 message_end，而不是继续拼接。
// 3. 断线指数退避，但绝不自动重发有副作用的命令。

import type {
  EventMessage,
  Message,
  Method,
  PiEvent,
  Request,
} from "@/types/protocol";

const MAX_BACKOFF_MS = 10_000;
const INITIAL_BACKOFF_MS = 500;

interface StreamHandlers {
  onDelta(text: string): void;
  onStart(): void;
  onSettled(): void;
  onStatus(status: string): void;
}

export class StreamClient {
  private ws: WebSocket | null = null;
  private backoff = INITIAL_BACKOFF_MS;
  private timer: number | undefined;
  private seq = 0;
  private disposed = false;

  constructor(private readonly handlers: StreamHandlers) {}

  connect(): void {
    if (this.disposed) return;
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`);
    this.ws = ws;

    ws.onopen = () => {
      this.backoff = INITIAL_BACKOFF_MS;
      setConnState(true);
      const sessionId = currentSessionId();
      if (sessionId) this.send("session.subscribe", sessionId);
    };
    ws.onclose = () => {
      setConnState(false);
      this.ws = null;
      if (this.disposed) return;
      this.timer = window.setTimeout(() => this.connect(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF_MS);
    };
    ws.onerror = () => ws.close();
    ws.onmessage = (event: MessageEvent<string>) => {
      let msg: Message;
      try {
        msg = JSON.parse(event.data) as Message;
      } catch {
        return;
      }
      this.handle(msg);
    };
  }

  dispose(): void {
    this.disposed = true;
    if (this.timer !== undefined) window.clearTimeout(this.timer);
    this.ws?.close();
    this.ws = null;
  }

  /** 发送命令。requestId 单调递增，保证连接内不重复。 */
  send(method: Method, sessionId: string, params?: unknown): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    const req: Request = {
      version: 1,
      kind: "command",
      requestId: `ui-${Date.now()}-${++this.seq}`,
      sessionId,
      method,
      ...(params === undefined ? {} : { params }),
    };
    this.ws.send(JSON.stringify(req));
  }

  private handle(msg: Message): void {
    if (msg.kind !== "event") return;
    const event = msg as EventMessage;
    if (event.event !== "pi.event") return;
    const ev = event.data as PiEvent | undefined;
    if (!ev) return;

    switch (ev.type) {
      case "agent_start":
        resetLive();
        this.handlers.onStart();
        this.handlers.onStatus("运行中");
        break;
      case "message_update": {
        const inner = ev.assistantMessageEvent;
        if (inner?.type === "text_delta" && inner.delta) {
          this.handlers.onDelta(inner.delta);
        }
        break;
      }
      case "agent_settled":
        this.handlers.onStatus("空闲");
        this.handlers.onSettled();
        break;
      default:
        break;
    }
  }
}

export function currentSessionId(): string {
  return document.body.dataset.sessionId ?? "";
}

export function setConnState(online: boolean): void {
  const el = document.getElementById("conn-state");
  if (!el) return;
  el.textContent = online ? "已连接" : "未连接";
  el.className = `state ${online ? "state-online" : "state-offline"}`;
}

function resetLive(): void {
  const live = document.getElementById("live");
  if (live) live.innerHTML = "";
}

/** 增量追加：只用 textContent，不碰已有节点的 innerHTML。 */
export function appendDelta(text: string): void {
  const live = document.getElementById("live");
  if (!live) return;
  let node = live.querySelector<HTMLElement>("[data-stream]");
  if (!node) {
    node = document.createElement("div");
    node.className = "bubble";
    node.setAttribute("data-stream", "");
    live.appendChild(node);
  }
  node.appendChild(document.createTextNode(text));
}

let client: StreamClient | null = null;

/** 挂载流式层。重复调用会先释放旧实例。 */
export function connectStream(): void {
  client?.dispose();
  client = new StreamClient({
    onDelta: appendDelta,
    onStart: () => {
      const live = document.getElementById("live");
      live?.classList.add("thinking");
    },
    onSettled: () => {
      const live = document.getElementById("live");
      live?.classList.remove("thinking");
      // 等权威 message_end 后再整轮重取，避免把半轮塞进折叠组。
      const turns = document.getElementById("turns");
      if (turns) window.htmx.trigger(turns, "load");
    },
    onStatus: (status) => {
      const el = document.getElementById("session-state");
      if (el) el.textContent = status;
    },
  });
  client.connect();
}
