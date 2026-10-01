// 图片附件。浏览器侧只做读取、体积预检与 base64 编码；
// 真正的格式/体积校验在桥的 pi.DecodeImages 里做，两侧都不信任对方。
//
// 上限与桥保持一致：张数 8、单张解码后 8 MB、base64 文本 12 MB。
// 这里先拦一道，避免把明显过大的文件读进内存再被桥拒绝。
const MAX_IMAGES = 8;
const MAX_BYTES = 8 * 1024 * 1024;
const ALLOWED = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);
/**
 * WIRE_BUDGET 是整条消息的图片 base64 预算。
 *
 * 必须与桥的 WS 读上限对齐（pi.MaxImages × pi.MaxImageDataLen + 封套余量）。
 * 旧实现只按「单张」判断，8 张 8 MB 图片编成 base64 后约 96 MB，
 * 远超桥的读限——不仅发不出去，超限帧还会直接把连接断开（U05）。
 * 在发送前按总量拦一次，让失败是可读的提示而不是断线。
 */
export const WIRE_BUDGET = MAX_IMAGES * 12 * 1024 * 1024;

export interface Attachment { name: string; mimeType: string; data: string; size: number }

function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error('读取文件失败'));
    reader.readAsDataURL(file);
  });
}

// addFiles 把 File 列表转成附件；返回被拒绝的原因，便于一次性告知用户。
export async function addFiles(files: FileList | File[], current: Attachment[]): Promise<{ added: Attachment[]; rejected: string[] }> {
  const added: Attachment[] = [];
  const rejected: string[] = [];
  for (const file of Array.from(files)) {
    if (current.length + added.length >= MAX_IMAGES) { rejected.push(`${file.name}: 最多 ${MAX_IMAGES} 张`); continue; }
    if (!ALLOWED.has(file.type)) { rejected.push(`${file.name}: 格式不受支持`); continue; }
    if (file.size > MAX_BYTES) { rejected.push(`${file.name}: 超过 ${Math.floor(MAX_BYTES / 1024 / 1024)} MB`); continue; }
    try {
      const url = await readAsDataURL(file);
      const comma = url.indexOf(',');
      added.push({ name: file.name, mimeType: file.type, data: url.slice(comma + 1), size: file.size });
    } catch (error) {
      rejected.push(`${file.name}: ${error instanceof Error ? error.message : '读取失败'}`);
    }
  }
  return { added, rejected };
}

// toWire 把附件转成桥的 Image 形状；data URL 前缀已在 addFiles 剥掉。
export function toWire(items: Attachment[]): { type: 'image'; data: string; mimeType: string }[] {
  return items.map((item) => ({ type: 'image', data: item.data, mimeType: item.mimeType }));
}

// formatSize 用于缩略图角标。
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export const limits = { MAX_IMAGES, MAX_BYTES, WIRE_BUDGET };
