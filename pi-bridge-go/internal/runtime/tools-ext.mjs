// 桥内工具集扩展：工具激活与 shell 命令的超时兜底。
//
// 一、把 Pi 当前注册的**全部**工具设为激活。
// 为什么需要它：Pi 默认只激活 read/bash/edit/write；grep/find/ls 虽已注册
// 但默认关闭。桥希望模型拿到全部工具（含 grep/find/ls 与已装插件注册的工具），
// 而不使用 `--tools` 白名单——那条路会**连带禁用扩展工具**（magic-context 的
// ctx_*、pi-goal-x 的 goal 工具会一起消失），`--exclude-tools` 也无法“额外启用”。
// 做法：会话启动时用 pi.getAllTools() 取全部已注册工具名，setActiveTools() 一次性激活。
// 插件工具可能在 session_start 之后才注册，因此下一个宏任务再校正一次，
// 并在 before_agent_start 再对齐一次（每轮开始前）。
//
// 二、shell 命令默认超时。
// Pi 的 bash/powershell 只在模型显式传 timeout 时才计时（参数说明原文是
// "no default timeout"），而模型几乎从不传；一条挂起的命令会让会话永久停在
// 工具执行里，只能手动中止。这里在 tool_call 上给缺省的调用补一个上限
// （codemode 脚本发出的嵌套调用同样经过 tool_call），并在提示词规则里告诉模型
// 这个默认值、要求预计更久的命令自己传 timeout。超时后 Pi 会杀掉整个进程组，
// 模型收到 "Command timed out after 300 seconds"。
const isTool = (tool) => tool && typeof tool.name === "string";

const DEFAULT_TIMEOUT_SECONDS = 300;
const SHELL_TOOLS = ["bash", "powershell"];
const TIMEOUT_GUIDELINE =
  `未指定 timeout 的 bash/powershell 命令会在 ${DEFAULT_TIMEOUT_SECONDS} 秒后被强制终止` +
  "（工具参数说明里的「无默认超时」在本环境不适用）。" +
  `预计会超过 ${DEFAULT_TIMEOUT_SECONDS} 秒的命令（完整构建、全量测试、依赖安装、大文件下载等）` +
  "必须显式传 timeout（单位秒）；服务、watch 等常驻进程不要在前台等待，改用 nohup … & 放到后台。";

export default function bridgeToolset(pi) {
  const apply = () => {
    try {
      const names = pi.getAllTools().filter(isTool).map((tool) => tool.name);
      if (names.length) pi.setActiveTools(names);
    } catch {
      /* 工具注册表尚未就绪等情况：留给下一次钩子重试 */
    }
  };

  pi.on("session_start", () => {
    apply();
    setImmediate(apply);
  });

  pi.on("before_agent_start", (event) => {
    apply();
    const options = event?.systemPromptOptions;
    if (!options || typeof options !== "object") return;
    const guidelines = (options.toolGuidelines ??= {});
    for (const name of SHELL_TOOLS) {
      const rules = (guidelines[name] ??= []);
      if (!rules.includes(TIMEOUT_GUIDELINE)) rules.push(TIMEOUT_GUIDELINE);
    }
  });

  // tool_call 处理器抛错会让 Pi 拦下这次调用，所以只在输入形态确定时改写。
  // null 与缺省同义：Pi 会把 null 当作非法超时直接报错。
  pi.on("tool_call", (event) => {
    if (!SHELL_TOOLS.includes(event?.toolName)) return;
    const input = event.input;
    if (!input || typeof input !== "object" || Array.isArray(input)) return;
    if (input.timeout == null) input.timeout = DEFAULT_TIMEOUT_SECONDS;
  });
}
