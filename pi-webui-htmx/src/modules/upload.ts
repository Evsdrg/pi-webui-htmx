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

interface UploadReply { path: string; size: number }

/** 一次上传的结果；被拒绝的文件单独列出，便于一次性告知。 */
export interface UploadOutcome {
  uploaded: string[];
  rejected: string[];
}

function uploadOne(dir: string, file: File, signal: AbortSignal): Promise<string> {
  const body = new FormData();
  body.set('dir', dir);
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
 * uploadFiles 逐个上传到 dir。
 *
 * 串行而不是并发：桥对同名文件是「先到先得」，并发上传两个同名文件时
 * 谁成功取决于调度，用户看到的提示会自相矛盾。串行让结果与选择顺序一致。
 */
export async function uploadFiles(dir: string, files: FileList | File[], signal: AbortSignal): Promise<UploadOutcome> {
  const outcome: UploadOutcome = { uploaded: [], rejected: [] };
  if (!dir) {
    outcome.rejected.push('还没有确定工作目录');
    return outcome;
  }
  for (const file of Array.from(files)) {
    if (signal.aborted) break;
    try {
      const saved = await uploadOne(dir, file, signal);
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
