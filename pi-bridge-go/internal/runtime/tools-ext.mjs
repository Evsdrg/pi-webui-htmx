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
// 措辞必须与**工具 schema 里的 timeout 说明**一致，不能再说「参数说明不适用」。
//
// 旧措辞是「未指定 timeout 的命令会在 300 秒后被终止（工具参数说明里的
// 『无默认超时』在本环境不适用）」——把一处矛盾写进了提示词：模型同时看到
// 「默认不设超时」与「300 秒后终止」，只能猜。实测这种猜是真金白银的：
// 历史 58586 次 bash 调用里 timeout 取值散落在 17 种上（300/None/120/180/600…）。
//
// 现在两边说同一件事：默认 300 秒（由本扩展在 tool_call 里补齐），要更久
// 就自己传。schema 那一侧的说明由 zh-system-prompt 的词典同步改成一致口径。
const TIMEOUT_GUIDELINE =
  `bash/powershell 的 timeout 默认 ${DEFAULT_TIMEOUT_SECONDS} 秒（本环境已为未指定的命令自动补上）。` +
  `预计会超过 ${DEFAULT_TIMEOUT_SECONDS} 秒的命令（完整构建、全量测试、依赖安装、大文件下载等）` +
  "必须显式传更大的 timeout（单位秒）；服务、watch 等常驻进程不要在前台等待，改用 nohup … & 放到后台。";

// 不要再给命令加「cd 到当前目录」的前缀。
//
// 为什么值得单独一条：实测 58586 次 bash 调用里 77.7% 以 `cd` 开头，其中
// 11558 次（19.7%）cd 的目标就是会话的 cwd 本身——纯粹的重复。模型这么做
// 是在模仿它见过的脚本，而不是因为需要：bash 工具本身就在 cwd 里执行。
// 这不只是观感问题：每个前缀都是模型要生成的 token，也让人误以为会话正
// 处在别的目录。
const CWD_GUIDELINE =
  "bash 已经在当前工作目录里执行，不要写 `cd <当前目录> &&` 这样的前缀。" +
  "确实要换目录时，用 `cd <目标目录> && …`（每次调用都是独立的 shell，不会留存状态）。";

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
    // cd 那条只挂在 bash 下：powershell 里是 Set-Location，措辞不同，
    // 硬套过去会让模型按 bash 的语义写 PowerShell。
    const bashRules = (guidelines.bash ??= []);
    if (!bashRules.includes(CWD_GUIDELINE)) bashRules.push(CWD_GUIDELINE);
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
