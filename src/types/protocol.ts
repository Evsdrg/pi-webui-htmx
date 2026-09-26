// 桥的 v1 协议类型。必须与 pi-bridge-go 的 internal/protocol/protocol.go
// 以及 api/v1/protocol.md 保持一致；改这里等于改契约，需要同步桥。
//
// 生成方式：目前手工维护。若字段频繁漂移，应改为从 Go 结构体生成。

export const PROTOCOL_VERSION = 1;

/** 命令：浏览器 → 桥。 */
export interface Request {
  version: number;
  kind: "command";
  requestId: string;
  sessionId?: string;
  method: Method;
  params?: unknown;
}

/** 响应：桥 → 浏览器，对应一次命令。 */
export interface Reply {
  version: number;
  kind: "response";
  requestId: string;
  ok: boolean;
  data?: unknown;
  error?: ProtocolError;
}

/** 事件：桥 → 浏览器，对应订阅推送。 */
export interface EventMessage {
  version: number;
  kind: "event";
  sessionId: string;
  streamId: string;
  epoch: string;
  seq: number;
  event: string;
  data?: unknown;
}

/** 控制帧：订阅关闭、终端关闭等。 */
export interface ControlMessage {
  version: number;
  kind: "control";
  sessionId?: string;
  event: string;
  data?: unknown;
}

export type Message = Reply | EventMessage | ControlMessage;

export interface ProtocolError {
  code: ErrorCode;
  message: string;
}

/** 错误码全集。桥不会返回集合外的码。 */
export type ErrorCode =
  | "invalid_request"
  | "unsupported_version"
  | "unsupported_method"
  | "invalid_params"
  | "not_found"
  | "conflict"
  | "busy"
  | "limit_exceeded"
  | "worker_not_running"
  | "worker_exited"
  | "timeout"
  | "outcome_unknown"
  | "pi_error"
  | "resync_required"
  | "internal"
  | "unauthorized"
  | "host_denied"
  | "origin_denied";

/** 桥实现的全部命令。capabilities.methods 必须覆盖这些。 */
export type Method =
  | "worker.list"
  | "session.start"
  | "session.state"
  | "session.prompt"
  | "session.abort"
  | "session.stop"
  | "session.subscribe"
  | "session.unsubscribe"
  | "session.steer"
  | "session.follow_up"
  | "session.set_queue_mode"
  | "session.models"
  | "session.set_model"
  | "session.cycle_model"
  | "session.thinking_levels"
  | "session.set_thinking"
  | "session.cycle_thinking"
  | "session.compact"
  | "session.set_auto_compaction"
  | "session.set_auto_retry"
  | "session.abort_retry"
  | "session.new"
  | "session.switch"
  | "session.fork"
  | "session.clone"
  | "session.tree"
  | "session.fork_messages"
  | "session.entries"
  | "session.bash"
  | "session.abort_bash"
  | "session.bash_output"
  | "session.ui_response"
  | "session.pending_dialogs"
  | "session.stats"
  | "session.set_name"
  | "session.last_assistant"
  | "session.commands"
  | "session.export_html"
  | "sessions.search"
  | "sessions.delete"
  | "config.models"
  | "config.models.raw"
  | "config.models.write"
  | "config.models.discover"
  | "config.models.test"
  | "config.catalog"
  | "config.packages"
  | "config.settings"
  | "config.trust"
  | "terminal.open"
  | "terminal.input"
  | "terminal.resize"
  | "terminal.close"
  | "terminal.list"
  | "files.list"
  | "files.stat"
  | "files.read"
  | "files.roots"
  | "git.status"
  | "git.diff";

/** capabilities 响应。UI 加载时据此判断兼容性。 */
export interface Capabilities {
  version: number;
  phase: string;
  piBaseline: string;
  methods: Method[];
  replay: boolean;
  persistentDedup: boolean;
  extensionDialogs: string;
  relay: boolean;
  history: string;
  limits: Record<string, number>;
}

// ---------------------------------------------------------------------------
// Pi 事件载荷
// ---------------------------------------------------------------------------

/** assistant 消息增量事件的子类型。 */
export type AssistantEvent =
  | { type: "text_delta"; contentIndex: number; delta: string }
  | { type: "thinking_delta"; contentIndex: number; delta: string }
  | { type: "toolcall_start"; contentIndex: number; id: string; name: string }
  | { type: "toolcall_delta"; contentIndex: number; delta: string }
  | { type: "toolcall_end"; contentIndex: number }
  | { type: "done"; contentIndex: number };

export interface PiEvent {
  type: string;
  id?: string;
  /** extension_ui_request 的方法名。 */
  method?: string;
  assistantMessageEvent?: AssistantEvent;
  steering?: string[];
  followUp?: string[];
}

// ---------------------------------------------------------------------------
// 扩展 UI 通道
// ---------------------------------------------------------------------------

/** 需要回执的扩展对话方法。 */
export type DialogMethod = "select" | "confirm" | "input" | "editor";

/** 无需回执的扩展 UI 方法。 */
export type FireAndForgetMethod =
  | "setStatus"
  | "setWidget"
  | "notify"
  | "setTitle"
  | "set_editor_text";

export type ExtensionMethod = DialogMethod | FireAndForgetMethod;

/** extension_ui_request 的载荷。字段随 method 变化。 */
export interface ExtensionUiRequest {
  type: "extension_ui_request";
  id: string;
  method: ExtensionMethod;
  // select
  title?: string;
  options?: string[];
  // confirm
  message?: string;
  // input / editor
  placeholder?: string;
  prefill?: string;
  timeout?: number;
  // setStatus
  statusKey?: string;
  statusText?: string;
  // setWidget
  widgetKey?: string;
  widgetLines?: string[];
  widgetPlacement?: string;
  // notify
  notifyType?: string;
  // setTitle
  // set_editor_text
  text?: string;
}

/** session.ui_response 的参数。三者语义互斥：cancelled 优先。 */
export interface UiResponseParams {
  id: string;
  value?: string;
  confirmed?: boolean;
  cancelled?: boolean;
}

// ---------------------------------------------------------------------------
// 业务数据结构（对应桥响应给模板的数据）
// ---------------------------------------------------------------------------

export interface SessionHeader {
  type: string;
  version: number;
  id: string;
  cwd: string;
  name?: string;
  timestamp: string;
  modified: string;
}

export interface SessionListing {
  items: SessionHeader[];
  hasMore: boolean;
  truncated: boolean;
}

export type EntryKind = "user" | "assistant" | "tool" | "compaction" | "other";

export interface Entry {
  id: string;
  kind: EntryKind;
  text: string;
}

export interface HistoryPage {
  sessionId: string;
  leafId: string;
  leafSource: string;
  entries: Entry[];
  oldestEntryId: string;
  hasMore: boolean;
}

export interface ModelRow {
  id: string;
  name: string;
  provider: string;
}

export interface PackageRow {
  name: string;
  source: string;
  version?: string;
  latest?: string;
  hasUpdate: boolean;
  disabled: boolean;
  error?: string;
}

export interface FileRow {
  name: string;
  path: string;
  isDir: boolean;
  size: string;
}

export interface WorkerInfo {
  sessionId: string;
  epoch: string;
  pid: number;
  cwd: string;
  status: WorkerStatus;
  busy: boolean;
  seq: number;
}

export type WorkerStatus =
  | "starting"
  | "idle"
  | "running"
  | "waiting_input"
  | "stopping"
  | "stopped"
  | "failed";
