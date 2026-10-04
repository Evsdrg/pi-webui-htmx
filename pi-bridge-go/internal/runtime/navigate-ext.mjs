// 桥内扩展：会话内跳转（session.navigate 的 Pi 侧实现）。
//
// 桥以 `pi -e <此文件>` 随进程下发（显式 -e 不受 --no-extensions 影响）。
// 桥发来的 prompt 文本形如 `/pi-webui-navigate <entryId>`，由 Pi 的
// 扩展命令分流截获：不发给模型、不写 transcript。RPC 的命令上下文只
// 返回 cancelled，拿不到叶子信息，所以这里自己读写 sessionManager，
// 把结果原子写入 PI_WEBUI_NAV_RESULT 指定的结果文件（每 worker 一份）。
import { renameSync, writeFileSync } from "node:fs";

const COMMAND = "pi-webui-navigate";

// 结果文件与桥之间是一次性握手：先写临时文件再改名，避免桥读到半截 JSON。
function writeResult(payload) {
  const path = process.env.PI_WEBUI_NAV_RESULT;
  if (!path) {
    return;
  }
  const tmp = `${path}.tmp`;
  writeFileSync(tmp, JSON.stringify(payload), { mode: 0o600 });
  renameSync(tmp, path);
}

export default function bridgeNavigate(pi) {
  pi.registerCommand(COMMAND, {
    description: "桥内部命令：把会话叶子跳转到指定条目（不发给模型）",
    handler: async (args, ctx) => {
      const target = String(args ?? "").trim();
      try {
        const before = ctx.sessionManager.getLeafId();
        const result = await ctx.navigateTree(target);
        if (result?.cancelled) {
          writeResult({ targetId: target, ok: false, cancelled: true, error: "跳转被扩展取消" });
          return;
        }
        writeResult({
          targetId: target,
          ok: true,
          newLeafId: ctx.sessionManager.getLeafId(),
          oldLeafId: before,
        });
      } catch (err) {
        writeResult({
          targetId: target,
          ok: false,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    },
  });
}
