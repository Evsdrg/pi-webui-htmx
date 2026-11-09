// 上传：把用户选中的文件送到桥的 POST /ui/files/upload。
//
// 为什么走 HTTP 而不是复用 WS：上传是流式的、体积不受 WS 单帧预算约束，
// 且它是桥里唯一的写入口——把它放在一条独立、可单独审计的路由上，
// 比塞进命令分发更清楚。
//
// 契约（与桥逐一对应，见 pi-bridge-go/internal/transport/upload.go）：
//   - 不覆盖已存在文件（桥回 409）
//   - 目标必须落在工作区根内且父目录已存在（桥回 403 / 404）
//   - 受保护目录（二进制、UI 发布目录、状态目录）一律 403
//   - 单文件上限与桥的读取上限同源（默认 4 MiB）
//
// 这里不做本地体积预检：上限由桥的配置决定，前端拿 capabilities 里的
// wsRequestBytes 去猜只会猜错（那是 WS 帧预算，不是上传上限）。

import { showToast } from './toast';
import type { UploadTarget } from './upload-target';
import { effectiveBase } from './upload-target';

interface UploadReply { path: string; size: number }

/** 一次上传的结果；被拒绝的文件单独列出，便于一次性告知。 */
export interface UploadOutcome {
  uploaded: string[];
  rejected: string[];
}

/** 上传目的地：当前目录，或固定目标（基路径 + 会话 id）。 */
export type UploadDestination =
  | { kind: 'cwd'; dir: string }
  | { kind: 'fixed'; base: string; sessionId: string };

function uploadOne(dest: UploadDestination, file: File, signal: AbortSignal): Promise<string> {
  const body = new FormData();
  if (dest.kind === 'cwd') {
    body.set('dir', dest.dir);
  } else {
    body.set('base', dest.base);
    body.set('sessionId', dest.sessionId);
  }
  body.set('file', file, file.name);
  return fetch('ui/files/upload', {
    method: 'POST',
    body,
    credentials: 'same-origin',
    signal,
  }).then(async (response) => {
    const text = await response.text();
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = undefined;
    }
    const payload = parsed as { ok?: boolean; error?: { message?: string }; path?: string } | undefined;
    if (!response.ok || payload?.ok === false) {
      const reason = payload?.error?.message ?? `HTTP ${response.status}`;
      throw new Error(reason);
    }
    return (payload as UploadReply).path ?? file.name;
  });
}

/**
 * destFromTarget 把设置里的上传目标解析成实际请求目的地。
 *
 * 固定目标需要会话 id：它被拼进路径（<base>/piwebui/<会话id>/），
 * 因此没有会话时不猜——直接报错让用户先选会话，而不是把文件塞进
 * 一个猜出来的目录。
 */
export function destFromTarget(target: UploadTarget, cwd: string, sessionId: string): UploadDestination | string {
  if (target.toCurrent) {
    if (!cwd) return '还没有确定工作目录';
    return { kind: 'cwd', dir: cwd };
  }
  if (!sessionId) return '统一目标目录需要先选中一个会话（文件名里要用会话 id）';
  return { kind: 'fixed', base: effectiveBase(target), sessionId };
}

/**
 * 目的地来源：固定值，或每个文件现算一次的函数。
 *
 * 为什么要允许函数：固定目标把会话 id 拼进路径，而用户完全可能在一批
 * 文件传完之前切换会话或切换目录。整批共用一个快照会让剩下的文件落进
 * 「已经离开的那个会话/目录」，用户在新位置永远找不到它们。现算的
 * 语义是「文件落到你此刻看到的那个位置」，与按钮/拖放所在位置一致。
 */
export type DestinationSource = UploadDestination | string | ((file: File) => UploadDestination | string);

/**
 * uploadFiles 逐个上传。
 *
 * 串行而不是并发：桥对同名文件是「先到先得」，并发上传两个同名文件时
 * 谁成功取决于调度，用户看到的提示会自相矛盾。串行让结果与选择顺序一致。
 */
export async function uploadFiles(dest: DestinationSource, files: FileList | File[], signal: AbortSignal): Promise<UploadOutcome> {
  const outcome: UploadOutcome = { uploaded: [], rejected: [] };
  for (const file of Array.from(files)) {
    if (signal.aborted) break;
    const resolved = typeof dest === 'function' ? dest(file) : dest;
    if (typeof resolved === 'string') {
      outcome.rejected.push(resolved);
      return outcome;
    }
    try {
      const saved = await uploadOne(resolved, file, signal);
      outcome.uploaded.push(saved);
    } catch (error) {
      if (signal.aborted) break;
      const reason = error instanceof Error ? error.message : '上传失败';
      outcome.rejected.push(`${file.name}: ${reason}`);
    }
  }
  return outcome;
}

/** reportUpload 把结果变成一条可读的提示。 */
export function reportUpload(outcome: UploadOutcome): void {
  if (outcome.rejected.length > 0) {
    // 部分成功也要把失败原因说出来：只说「上传完成」会让用户以为全成了。
    const ok = outcome.uploaded.length > 0 ? `已上传 ${outcome.uploaded.length} 个；` : '';
    showToast(`${ok}${outcome.rejected.join('；')}`, 'error', 9000);
    return;
  }
  if (outcome.uploaded.length === 0) return;
  showToast(`已上传 ${outcome.uploaded.length} 个文件`, 'info');
}
