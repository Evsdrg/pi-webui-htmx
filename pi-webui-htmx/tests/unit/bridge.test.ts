import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BridgeClient, decodeMessage } from '../../src/modules/bridge';

class Socket {
  readyState = 0; bufferedAmount = 0; sent: string[] = [];
  onopen: (() => void) | null = null; onclose: (() => void) | null = null;
  onerror: (() => void) | null = null; onmessage: ((event: {data:string}) => void) | null = null;
  open() { this.readyState = 1; this.onopen?.(); }
  close() { this.readyState = 3; this.onclose?.(); }
  send(value: string) { this.sent.push(value); }
  reply(index: number, data: unknown, ok = true) {
    const request = JSON.parse(this.sent[index]!);
    this.onmessage?.({data:JSON.stringify({version:1,kind:'response',requestId:request.requestId,ok,...(ok ? {data} : {error:data})})});
  }
}
let client: BridgeClient; let sockets: Socket[];
beforeEach(() => { vi.useFakeTimers(); sockets = []; client = new BridgeClient('ws://localhost/api/v1/ws', () => { const s = new Socket(); sockets.push(s); return s as unknown as WebSocket; }); });
afterEach(() => { client.dispose(); vi.useRealTimers(); });
function connect() { client.connect(); sockets[0]!.open(); return sockets[0]!; }

describe('桥命令回执', () => {
  it('未连接时明确拒绝，不静默丢弃', async () => { await expect(client.request('session.prompt','s',{text:'test'})).rejects.toMatchObject({code:'disconnected'}); expect(client.pendingCount).toBe(0); });
  it('乱序回执按 requestId 关联', async () => { const s = connect(); const first = client.request('worker.list'); const second = client.request('files.roots'); s.reply(1,{roots:['/work']}); s.reply(0,[]); await expect(first).resolves.toEqual([]); await expect(second).resolves.toEqual({roots:['/work']}); expect(client.pendingCount).toBe(0); });
  it('返回可见错误并释放等待项', async () => { const s = connect(); const request = client.request('files.read','',{path:'/bad'}); s.reply(0,{code:'forbidden',message:'路径不被允许'},false); await expect(request).rejects.toMatchObject({code:'forbidden',message:'路径不被允许'}); expect(client.pendingCount).toBe(0); });
  it('超时报告结果未知，绝不重发', async () => { const s = connect(); const request = client.request('session.prompt','s',{text:'hello'},100); const assertion = expect(request).rejects.toMatchObject({code:'outcome_unknown'}); await vi.advanceTimersByTimeAsync(101); await assertion; expect(s.sent).toHaveLength(1); expect(client.pendingCount).toBe(0); });
  it('断线重连不重发有副作用命令', async () => { const s = connect(); const request = client.request('session.prompt','s',{text:'hello'}); const assertion = expect(request).rejects.toMatchObject({code:'outcome_unknown'}); s.close(); await assertion; await vi.advanceTimersByTimeAsync(500); expect(sockets).toHaveLength(2); sockets[1]!.open(); expect(sockets[1]!.sent).toHaveLength(0); });
  it('销毁后关闭套接字并取消重连计时器', async () => { const s = connect(); s.close(); client.dispose(); await vi.advanceTimersByTimeAsync(30_000); expect(sockets).toHaveLength(1); expect(vi.getTimerCount()).toBe(0); });
  it('对在途请求和浏览器发送缓冲分别限额', async () => { const s = connect(); s.bufferedAmount = 600_000; await expect(client.request('worker.list')).rejects.toMatchObject({code:'limit_exceeded'}); s.bufferedAmount = 0; const waiting = Array.from({length:16}, () => client.request('worker.list').catch(() => {})); await expect(client.request('worker.list')).rejects.toMatchObject({code:'limit_exceeded'}); client.dispose(); await Promise.all(waiting); expect(client.pendingCount).toBe(0); });
  it('拒绝损坏帧、未知版本和不合法响应结构', () => { for (const value of ['{','null','[]','{"version":2,"kind":"event","event":"pi.event"}','{"version":1,"kind":"response","ok":true}']) expect(decodeMessage(value)).toBeNull(); });
});
