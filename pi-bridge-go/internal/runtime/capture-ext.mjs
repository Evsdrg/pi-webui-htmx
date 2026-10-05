// 桥内捕获扩展：把 Pi 实际发给模型的系统提示词与工具定义落盘，供 /ui/system
// 与 /ui/tools 显示。
//
// 为什么需要它：Pi 的 export_html 快照里 systemPrompt 是扩展改写之前的基线——
// 每轮结束 Pi 会把 AgentState.systemPrompt 复位成基线，而 RPC 命令表也没有
// 暴露最终载荷的命令。唯一能看到「真正发出去的那份」的位置是扩展的
// before_provider_request：它拿到的 event.payload 就是即将发给 provider 的载荷。
//
// 顺序：桥以 `-e` 下发本扩展，因此它在所有 agent-dir / 包扩展之前运行。
// 直接同步读取会漏掉其后的改写（同步与异步都有），所以这里延迟到下一个宏
// 任务再取，并在 agent_settled 再取一次以覆盖「等一个异步操作才原地改写」的插件。
//
// 结果按会话 id 分文件写入 PI_WEBUI_CAPTURE_DIR；桥按 worker 的会话 id 读取。
// 变量名不能用 PI_BRIDGE_ 前缀：childenv.Filter 会把它当作桥凭据剥掉。
import { writeFileSync, renameSync } from "node:fs";
import { join } from "node:path";

// systemText 按已知的 provider 载荷形态取出系统提示词所在的槽位，只认系统角色，
// 不碰对话消息。覆盖 openai-responses（instructions / input[role=developer]）、
// anthropic-messages（system 字符串或内容块数组）、google（systemInstruction）。
function systemText(payload) {
  const out = [];
  if (!payload || typeof payload !== "object") return "";
  if (typeof payload.instructions === "string") out.push(payload.instructions);
  if (typeof payload.system === "string") out.push(payload.system);
  if (Array.isArray(payload.system)) {
    for (const part of payload.system) if (part && typeof part.text === "string") out.push(part.text);
  }
  const gi = payload.systemInstruction;
  if (typeof gi === "string") out.push(gi);
  else if (gi && typeof gi === "object") {
    if (typeof gi.text === "string") out.push(gi.text);
    if (Array.isArray(gi.parts)) for (const part of gi.parts) if (part && typeof part.text === "string") out.push(part.text);
  }
  const messages = Array.isArray(payload.input) ? payload.input : Array.isArray(payload.messages) ? payload.messages : [];
  for (const message of messages) {
    if (!message || (message.role !== "system" && message.role !== "developer")) continue;
    if (typeof message.content === "string") out.push(message.content);
    else if (Array.isArray(message.content)) {
      for (const part of message.content) if (part && typeof part.text === "string") out.push(part.text);
    }
  }
  return out.join("\n\n===== SLOT =====\n\n");
}

// toolList 把各家 provider 的工具定义归一成 {name, description, parameters}。
// 覆盖 openai-responses（顶层 name）、openai-chat（function.name）、anthropic（input_schema）。
function toolList(payload) {
  const raw = payload && Array.isArray(payload.tools) ? payload.tools : [];
  const out = [];
  for (const tool of raw) {
    if (!tool || typeof tool !== "object" || out.length >= 512) continue;
    const fn = tool.function && typeof tool.function === "object" ? tool.function : null;
    const name = typeof tool.name === "string" ? tool.name : fn && typeof fn.name === "string" ? fn.name : "";
    if (!name) continue;
    const description = typeof tool.description === "string" ? tool.description : fn && typeof fn.description === "string" ? fn.description : "";
    const parameters = tool.parameters ?? tool.input_schema ?? (fn ? fn.parameters : undefined) ?? null;
    out.push({ name, description, parameters });
  }
  return out;
}

function sessionIdOf(ctx) {
  try {
    const manager = ctx && ctx.sessionManager;
    const get = manager && manager.getSessionId;
    const value = typeof get === "function" ? get.call(manager) : undefined;
    return typeof value === "string" ? value : "";
  } catch {
    return "";
  }
}

export default function bridgeCapture(pi) {
  const dir = process.env.PI_WEBUI_CAPTURE_DIR;
  if (!dir) return;
  let last = null;

  const write = (payload, ctx) => {
    if (!payload || typeof payload !== "object") return;
    const sid = sessionIdOf(ctx);
    // 会话 id 直接来自 Pi；再挡一次路径穿越，避免写出行外文件。
    if (!sid || sid.includes("/") || sid.includes("\\")) return;
    try {
      const record = JSON.stringify({
        sessionId: sid,
        systemPrompt: systemText(payload),
        tools: toolList(payload),
        at: Date.now(),
      });
      const path = join(dir, `${sid}.json`);
      const tmp = `${path}.tmp`;
      writeFileSync(tmp, record, { mode: 0o600 });
      renameSync(tmp, path);
    } catch {
      /* 落盘失败不影响主流程 */
    }
  };

  pi.on("before_provider_request", (event, ctx) => {
    last = event?.payload;
    // 延迟到下一个宏任务：此刻其它 before_provider_request 处理器已经同步跑完
    // （异步原地改写的由下面的 agent_settled 兜底），读到的才是最终载荷。
    setImmediate(() => write(last, ctx));
  });
  pi.on("agent_settled", (_event, ctx) => write(last, ctx));
}
