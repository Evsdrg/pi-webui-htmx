// 桥内工具集扩展：按桥下发的预设名单重设 Pi 的**激活工具**。
//
// 为什么需要它：Pi 的 `--tools` 是“只允许这些工具”的白名单，会**连带禁用
// 扩展工具**（magic-context 的 ctx_*、pi-goal-x 的 goal 工具会一起消失）；
// `--exclude-tools` 是 deny 列表，无法用来“额外启用默认关闭的 grep/find/ls”。
// 于是桥无法用 CLI 参数表达「只读 / 完整」这两个预设而不误伤插件。
//
// Pi 的扩展 API 提供 pi.getActiveTools() / pi.setActiveTools()：在会话启动时
// 把「预设期望的内置工具」与「当前所有扩展工具」求并集后重设，既不丢插件工具，
// 又能启用 grep/find/ls。
//
// 顺序：桥以 `-e` 下发本扩展，在所有 agent-dir / 包扩展**之前**运行；因此
// session_start 时插件工具可能尚未注册。这里监听 session_start 后，
// 在下一个宏任务再取一次，并在 before_agent_start 再校正一次，覆盖
// 「插件在更晚的钩子里才注册工具」的情形。
//
// 输入：PI_WEBUI_ACTIVE_TOOLS —— 逗号分隔的内置工具名单（空串表示保留扩展
// 工具但不启用任何内置工具）。未设置该变量时本扩展完全不介入（default/空预设）。
// 变量名不能用 PI_BRIDGE_ 前缀：childenv.Filter 会把它当作桥凭据剥掉。
const BUILTIN = ["read", "bash", "edit", "write", "grep", "find", "ls", "powershell"];

export default function bridgeToolset(pi) {
  const spec = process.env.PI_WEBUI_ACTIVE_TOOLS;
  if (spec === undefined) return;
  const wanted = spec.split(",").map((name) => name.trim()).filter(Boolean);

  const apply = () => {
    try {
      const all = pi.getAllTools().map((tool) => tool.name);
      const extensionTools = all.filter((name) => !BUILTIN.includes(name));
      const active = wanted.filter((name) => all.includes(name));
      // 并集去重：预设内置工具 + 全部扩展工具。
      pi.setActiveTools([...new Set([...active, ...extensionTools])]);
    } catch {
      /* 工具注册表尚未就绪等情况：留给下一次钩子重试 */
    }
  };

  pi.on("session_start", () => {
    apply();
    // 插件工具可能在 session_start 之后才注册，延后一拍再校正一次。
    setImmediate(apply);
  });
  // 每轮开始前再校正：插件在新一轮里新注册的工具也能被并进来。
  pi.on("before_agent_start", () => apply());
}
