import { describe, expect, it, vi } from 'vitest';
import { addFiles, toWire, formatSize, limits } from '@/modules/attachments';

// jsdom 的 FileReader 需要真实的 ArrayBuffer，这里构造最小 PNG。
function pngFile(name = 'a.png', type = 'image/png'): File {
  const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
  return new File([bytes], name, { type });
}

describe('图片附件', () => {
  it('读取文件并剥掉 data URL 前缀', async () => {
    const { added, rejected } = await addFiles([pngFile()], []);
    expect(rejected).toEqual([]);
    expect(added).toHaveLength(1);
    expect(added[0].mimeType).toBe('image/png');
    expect(added[0].data.startsWith('data:')).toBe(false);
    expect(added[0].data.length).toBeGreaterThan(0);
    expect(added[0].size).toBe(11);
  });

  it('拒绝不支持的格式并给出可读原因', async () => {
    for (const type of ['image/svg+xml', 'application/pdf', 'text/plain']) {
      const { added, rejected } = await addFiles([new File(['x'], 'f', { type })], []);
      expect(added).toHaveLength(0);
      expect(rejected[0]).toContain('格式不受支持');
    }
  });

  it('超过张数上限时拒绝且不读取文件', async () => {
    const existing = Array.from({ length: limits.MAX_IMAGES }, (_, i) => ({ name: `e${i}.png`, mimeType: 'image/png', data: 'x', size: 1 }));
    const { added, rejected } = await addFiles([pngFile()], existing);
    expect(added).toHaveLength(0);
    expect(rejected[0]).toContain(`最多 ${limits.MAX_IMAGES} 张`);
  });

  it('超过体积上限时拒绝', async () => {
    const big = new File([new Uint8Array(limits.MAX_BYTES + 1)], 'big.png', { type: 'image/png' });
    const { added, rejected } = await addFiles([big], []);
    expect(added).toHaveLength(0);
    expect(rejected[0]).toContain('MB');
  });

  it('toWire 输出 Pi 的 ImageContent 形状', () => {
    const wire = toWire([{ name: 'a.png', mimeType: 'image/png', data: 'QUJD', size: 3 }]);
    expect(wire).toEqual([{ type: 'image', data: 'QUJD', mimeType: 'image/png' }]);
  });

  it('formatSize 按量级切换单位', () => {
    expect(formatSize(512)).toBe('512 B');
    expect(formatSize(2048)).toBe('2 KB');
    expect(formatSize(3 * 1024 * 1024)).toBe('3.0 MB');
  });
});
