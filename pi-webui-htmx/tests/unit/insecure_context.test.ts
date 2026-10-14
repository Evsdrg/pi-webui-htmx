// 非安全上下文（HTTP + 非 localhost）下浏览器会把一批 API 从 window 上摘掉：
// crypto.randomUUID、navigator.clipboard、crypto.subtle 都不存在。
//
// 本仓的本地部署形态正是这个环境：桥绑在组网/局域网地址上，用户用
// http://<ip>:<port> 直接访问（桥不自带 TLS）。前端若不降级，会在模块加载阶段
// 就抛异常、整页停在初始状态——真实复现过：`crypto.randomUUID is not a function`
// 让 Workbench 构造失败，页面永远显示「连接中」。
import { afterEach, describe, expect, it, vi } from 'vitest';
import { randomId } from '@/lib/random';
import { copyText } from '@/lib/clipboard';
import { BridgeClient } from '@/modules/bridge';

const realCrypto = globalThis.crypto;

/** withInsecureCrypto 模拟非安全上下文：只有 getRandomValues，没有 randomUUID/subtle。
 *  用递增计数当伪随机源，让每次调用的输出不同（真实的 getRandomValues 就是随机的）。 */
function withInsecureCrypto() {
  let counter = 0;
  Object.defineProperty(globalThis, 'crypto', {
    configurable: true,
    value: {
      getRandomValues: (array: Uint8Array) => {
        counter += 1;
        let x = counter * 2654435761 % 4294967296;
        for (let i = 0; i < array.length; i++) { x = (x * 1103515245 + 12345) % 4294967296; array[i] = (x >>> 16) & 0xff; }
        return array;
      },
    },
  });
}

afterEach(() => {
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: realCrypto });
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe('非安全上下文仍要可用', () => {
  it('缺少 crypto.randomUUID 时 randomId 降级且保持唯一', () => {
    withInsecureCrypto();
    const ids = Array.from({ length: 50 }, () => randomId());
    for (const id of ids) expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('连 crypto 都没有时也不抛异常', () => {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: undefined });
    expect(() => randomId()).not.toThrow();
    expect(randomId().length).toBeGreaterThan(0);
  });

  it('BridgeClient 在缺少 crypto.randomUUID 时仍能构造（这正是整页卡住的根因）', () => {
    withInsecureCrypto();
    expect(() => new BridgeClient('ws://example/api/v1/ws')).not.toThrow();
  });

  it('没有 navigator.clipboard 时 copyText 退回 execCommand 而不是抛异常', async () => {
    vi.stubGlobal('navigator', { });
    document.execCommand = vi.fn(() => true);
    await expect(copyText('要复制的内容')).resolves.toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith('copy');
  });

  it('剪贴板被拒绝时返回 false，调用方据此给出可见提示', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: () => Promise.reject(new Error('denied')) } });
    document.execCommand = vi.fn(() => false);
    await expect(copyText('内容')).resolves.toBe(false);
  });
});
