// 相对 URL 助手（B54）。
//
// 同一份前端产物要同时用于两种部署形态：
//   - 本地：桥直接服务，文档在 `/`；
//   - 云端：relay 的 `/d/{deviceId}/` 前缀下，经隧道转发到桥。
//
// 因此所有请求（fetch / htmx / WebSocket）都必须相对**文档基地址**解析，
// 而不是写成根绝对路径——绝对路径在云端会打到 relay 的根，绕过设备前缀。
// 基地址由桥渲染的 `<base href>` 给出（见 presentation.DocBase）。

/** absoluteUrl 把相对路径解析成绝对 URL，基于文档基地址。 */
export function absoluteUrl(path: string): URL {
  return new URL(path.replace(/^\//, ''), document.baseURI);
}

/** relativeSocketUrl 把相对路径解析成 ws/wss URL，保持与文档同协议。 */
export function relativeSocketUrl(path: string): string {
  const url = absoluteUrl(path);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.toString();
}
