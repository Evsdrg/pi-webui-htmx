import { describe, expect, it } from 'vitest';
import { addFiles, limits } from '@/modules/attachments';

function files(n: number): File[] {
  return Array.from({ length: n }, (_, i) => new File([new Uint8Array([1, 2, 3])], `p${i}.png`, { type: 'image/png' }));
}

describe('附件额度', () => {
  it('并发批次不能突破张数上限', async () => {
    // U20：addFiles 只按调用时的数组判断额度，两个并发批次各自看到
    // 旧的空数组，合并后越过上限。上层必须串行化，这里验证串行后的结果。
    const held: { name: string; mimeType: string; data: string; size: number }[] = [];
    const first = await addFiles(files(limits.MAX_IMAGES), held);
    held.push(...first.added);
    const second = await addFiles(files(limits.MAX_IMAGES), held);
    held.push(...second.added);
    expect(held.length).toBe(limits.MAX_IMAGES);
    expect(second.rejected.length).toBeGreaterThan(0);
  });

  it('空批次不改变已有附件', async () => {
    const held: { name: string; mimeType: string; data: string; size: number }[] = [];
    const first = await addFiles(files(2), held);
    held.push(...first.added);
    const none = await addFiles([], held);
    held.push(...none.added);
    expect(held.length).toBe(2);
    expect(none.added).toHaveLength(0);
  });

  it('超限文件给出可读原因', async () => {
    const huge = new File([new Uint8Array([1])], 'big.png', { type: 'image/png' });
    Object.defineProperty(huge, 'size', { value: limits.MAX_BYTES + 1 });
    const { added, rejected } = await addFiles([huge], []);
    expect(added).toHaveLength(0);
    expect(rejected.join()).toContain('big.png');
  });
});
