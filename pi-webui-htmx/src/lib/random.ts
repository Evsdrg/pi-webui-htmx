// 随机标识助手。
//
// 为什么不能直接用 crypto.randomUUID()：它只在**安全上下文**（HTTPS 或
// localhost）里存在。本仓的本地部署形态是「桥绑在组网/局域网地址上，用户用
// http://<ip>:<port> 访问」（桥不自带 TLS），那是非安全上下文，randomUUID
// 是 undefined。前端若在模块加载阶段就调用它，会直接抛异常、整页停摆：
// 真实复现过 `crypto.randomUUID is not a function` 让 Workbench 构造失败。
//
// 降级只用 crypto.getRandomValues（非安全上下文里仍然提供）；若连它都没有，
// 再退到 Math.random。这里生成的是**去重用的标识**，不是安全令牌，因此
// 降级不削弱安全性质。

/** randomId 生成一个 UUID v4 形状的随机标识。 */
export function randomId(): string {
  // 显式写成 Uint8Array<ArrayBuffer>：crypto.getRandomValues 的签名不接受
  // 泛型参数更宽的视图（Uint8Array<ArrayBufferLike> 会被拒绝）。
  const bytes = new Uint8Array(new ArrayBuffer(16));
  fillRandom(bytes);
  // 按 RFC 4122 设置版本与变体位，形状与 randomUUID 一致。
  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex: string[] = [];
  for (const byte of bytes) hex.push(byte.toString(16).padStart(2, '0'));
  const s = hex.join('');
  return `${s.slice(0, 8)}-${s.slice(8, 12)}-${s.slice(12, 16)}-${s.slice(16, 20)}-${s.slice(20)}`;
}

function fillRandom(bytes: Uint8Array<ArrayBuffer>): void {
  const webCrypto = globalThis.crypto;
  if (webCrypto?.getRandomValues) {
    webCrypto.getRandomValues(bytes);
    return;
  }
  for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
}
