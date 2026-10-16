// 桥内配置重载扩展：让当前 Pi 进程重新读取配置（models.json 等）。
//
// 为什么需要它：Pi 的 ModelRuntime 在进程创建时只读一次 models.json，之后
// 新增的 provider/模型不会进入内存快照——get_available_models 与 set_model
// 都读那份快照，于是「刚添加的模型」会报 Model not found，直到工作进程重启。
// RPC 命令表里没有任何刷新模型的命令，但 RPC 模式给扩展的命令上下文提供了
// reload（rpc-mode 的 commandContextActions.reload → session.reload()，内部会
// resetApiProviders() 并重读配置与资源），因此经内部命令触发即可，不必重启
// 进程、也不丢会话身份与当前选中的模型。
//
// 与跳转扩展同样的约定：命令命中后不发给模型、不写 transcript；一次性的
// 结果经每 worker 一份的结果文件回传（prompt 的回执无法区分「handler 成功」
// 与「handler 抛错后仍算 handled」，所以必须有这条独立通道）。
// 变量名不能用 PI_BRIDGE_ 前缀：childenv.Filter 会把它当作桥凭据剥掉。
import { renameSync, writeFileSync } from "node:fs";

const COMMAND = "pi-webui-reload";

// 先写临时文件再改名，避免桥读到半截 JSON。
function writeResult(payload) {
  const path = process.env.PI_WEBUI_RELOAD_RESULT;
  if (!path) {
    return;
  }
  const tmp = `${path}.tmp`;
  writeFileSync(tmp, JSON.stringify(payload), { mode: 0o600 });
  renameSync(tmp, path);
}

export default function bridgeReload(pi) {
  pi.registerCommand(COMMAND, {
    description: "桥内部命令：让当前 Pi 进程重新读取配置（不发给模型）",
    handler: async (_args, ctx) => {
      try {
        // reload 之后旧 ctx 就失效了（runner 会 invalidate），因此不碰 ctx，
        // 只把结果写到文件——fs 与 env 与扩展上下文无关。
        await ctx.reload();
        writeResult({ ok: true });
      } catch (err) {
        writeResult({ ok: false, error: err instanceof Error ? err.message : String(err) });
      }
    },
  });
}
