// 桥内工具集扩展：把 Pi 当前注册的**全部**工具设为激活。
//
// 为什么需要它：Pi 默认只激活 read/bash/edit/write；grep/find/ls 虽已注册
// 但默认关闭。桥希望模型拿到全部工具（含 grep/find/ls 与已装插件注册的工具），
// 而不使用 `--tools` 白名单——那条路会**连带禁用扩展工具**（magic-context 的
// ctx_*、pi-goal-x 的 goal 工具会一起消失），`--exclude-tools` 也无法“额外启用”。
//
// 做法：会话启动时用 pi.getAllTools() 取全部已注册工具名，setActiveTools() 一次性激活。
// 插件工具可能在 session_start 之后才注册，因此下一个宏任务再校正一次，
// 并在 before_agent_start 再对齐一次（每轮开始前）。
const isTool = (tool) => tool && typeof tool.name === "string";

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
  pi.on("before_agent_start", () => apply());
}
