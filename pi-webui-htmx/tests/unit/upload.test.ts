// 上传：把文件送到桥的写入口。
// 桥那边「不覆盖 / 越界拒绝 / 受保护目录」的判定由 Go 测试钉住，
// 这里钉前端的三件事：请求形状、目标分流、失败原因原样呈现。
import { afterEach, expect, test, vi } from 'vitest';
import { destFromTarget, reportUpload, uploadFiles } from '@/modules/upload';
import { clearToasts } from '@/modules/toast';
import type { UploadTarget } from '@/modules/upload-target';

afterEach(() => { vi.unstubAllGlobals(); clearToasts(); document.body.replaceChildren(); });

const SESSION = '01a1ba81-1c77-72b4-bf0c-31c986eeab7b';

function okResponse(path: string): Response {
  return new Response(JSON.stringify({ ok: true, path, size: 5 }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function errResponse(status: number, message: string): Response {
  return new Response(JSON.stringify({ ok: false, error: { code: 'conflict', message } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const file = (name: string, content = 'hello') => new File([content], name, { type: 'text/plain' });
const cwdDest = (dir = '/repo') => ({ kind: 'cwd' as const, dir });

test('上传到当前目录：只带 dir', async () => {
  const fetchMock = vi.fn().mockResolvedValue(okResponse('/repo/a.txt'));
  vi.stubGlobal('fetch', fetchMock);
  const outcome = await uploadFiles(cwdDest(), [file('a.txt')], new AbortController().signal);
  expect(outcome.uploaded).toEqual(['/repo/a.txt']);
  expect(outcome.rejected).toEqual([]);
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(url).toBe('ui/files/upload');
  expect(init.method).toBe('POST');
  const body = init.body as FormData;
  expect(body.get('dir')).toBe('/repo');
  expect(body.get('base')).toBeNull();
  expect((body.get('file') as File).name).toBe('a.txt');
});

test('固定目标：带 base 与 sessionId，不带 dir', async () => {
  const fetchMock = vi.fn().mockResolvedValue(okResponse('/scratch/piwebui/x/a.txt'));
  vi.stubGlobal('fetch', fetchMock);
  await uploadFiles({ kind: 'fixed', base: '/scratch', sessionId: SESSION }, [file('a.txt')], new AbortController().signal);
  const body = (fetchMock.mock.calls[0] as [string, RequestInit])[1].body as FormData;
  expect(body.get('base')).toBe('/scratch');
  expect(body.get('sessionId')).toBe(SESSION);
  expect(body.get('dir')).toBeNull();
});

test('多个文件串行上传，顺序与选择一致', async () => {
  const calls: string[] = [];
  const fetchMock = vi.fn().mockImplementation((_url: string, init: RequestInit) => {
    const body = init.body as FormData;
    calls.push((body.get('file') as File).name);
    return Promise.resolve(okResponse('/repo/x'));
  });
  vi.stubGlobal('fetch', fetchMock);
  await uploadFiles(cwdDest(), [file('b.txt'), file('a.txt')], new AbortController().signal);
  expect(calls).toEqual(['b.txt', 'a.txt']);
});

test('被拒绝的文件保留桥给出的原因', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(errResponse(409, '目标文件已存在，上传不覆盖')));
  const outcome = await uploadFiles(cwdDest(), [file('dup.txt')], new AbortController().signal);
  expect(outcome.uploaded).toEqual([]);
  expect(outcome.rejected[0]).toContain('dup.txt');
  expect(outcome.rejected[0]).toContain('目标文件已存在');
});

test('部分成功时两边的结果都记下来', async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(okResponse('/repo/ok.txt'))
    .mockResolvedValueOnce(errResponse(403, '该目录不允许写入'));
  vi.stubGlobal('fetch', fetchMock);
  const outcome = await uploadFiles(cwdDest(), [file('ok.txt'), file('bad.txt')], new AbortController().signal);
  expect(outcome.uploaded).toEqual(['/repo/ok.txt']);
  expect(outcome.rejected[0]).toContain('该目录不允许写入');
});

test('中止后不再继续上传剩余文件', async () => {
  const controller = new AbortController();
  const fetchMock = vi.fn().mockImplementation(() => {
    controller.abort();
    return Promise.reject(new DOMException('aborted', 'AbortError'));
  });
  vi.stubGlobal('fetch', fetchMock);
  await uploadFiles(cwdDest(), [file('a.txt'), file('b.txt')], controller.signal);
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

test('部分失败时提示里同时给出成功数与失败原因', () => {
  reportUpload({ uploaded: ['/repo/a.txt'], rejected: ['b.txt: 目标文件已存在，上传不覆盖'] });
  const text = document.body.textContent ?? '';
  expect(text).toContain('已上传 1 个');
  expect(text).toContain('目标文件已存在');
});

// ---- 目标分流：三条规则与用户需求一一对应 ----

const target = (toCurrent: boolean, base = ''): UploadTarget => ({ toCurrent, base });

test('规则1：开着「上传到当前目录」时忽略基路径', () => {
  const dest = destFromTarget(target(true, '/scratch'), '/repo', SESSION);
  expect(dest).toEqual({ kind: 'cwd', dir: '/repo' });
});

test('规则2：关掉且未配置基路径 → 默认 /tmp/piwebui/<会话id>', () => {
  const dest = destFromTarget(target(false), '/repo', SESSION);
  expect(dest).toEqual({ kind: 'fixed', base: '/tmp', sessionId: SESSION });
});

test('规则3：关掉并配置基路径 → <基路径>/piwebui/<会话id>', () => {
  const dest = destFromTarget(target(false, '/scratch'), '/repo', SESSION);
  expect(dest).toEqual({ kind: 'fixed', base: '/scratch', sessionId: SESSION });
});

test('固定目标缺会话时明确报错，而不是猜一个目录', () => {
  const dest = destFromTarget(target(false, '/scratch'), '/repo', '');
  expect(typeof dest).toBe('string');
  expect(String(dest)).toContain('会话');
});

test('当前目录模式下没有工作目录时报错', () => {
  const dest = destFromTarget(target(true), '', SESSION);
  expect(typeof dest).toBe('string');
});

test('目的地是错误消息时不发请求', async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
  const outcome = await uploadFiles('还没有确定工作目录', [file('a.txt')], new AbortController().signal);
  expect(fetchMock).not.toHaveBeenCalled();
  expect(outcome.rejected.length).toBe(1);
});

// ---- 上传途中目标变化：目的地必须每个文件现算 ----

test('一批文件途中切换会话后，剩余文件落到新会话目录', async () => {
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(okResponse('/scratch/piwebui/x/a.txt')));
  vi.stubGlobal('fetch', fetchMock);
  // 模拟用户在上传第一个文件期间切了会话：dataset 变了。
  let reads = 0;
  const destOf = () => {
    reads++;
    const sessionId = reads === 1 ? 'old-session' : 'new-session';
    return { kind: 'fixed' as const, base: '/scratch', sessionId };
  };
  await uploadFiles(destOf, [file('a.txt'), file('b.txt')], new AbortController().signal);
  const first = (fetchMock.mock.calls[0] as [string, RequestInit])[1].body as FormData;
  const second = (fetchMock.mock.calls[1] as [string, RequestInit])[1].body as FormData;
  expect(first.get('sessionId')).toBe('old-session');
  expect(second.get('sessionId')).toBe('new-session');
});

test('目的地现算返回错误消息时，该文件记为失败且不再继续', async () => {
  const fetchMock = vi.fn().mockResolvedValue(okResponse('/repo/a.txt'));
  vi.stubGlobal('fetch', fetchMock);
  let calls = 0;
  const destOf = () => {
    calls++;
    return calls === 1 ? cwdDest() : '还没有确定工作目录';
  };
  const outcome = await uploadFiles(destOf, [file('a.txt'), file('b.txt')], new AbortController().signal);
  expect(outcome.uploaded).toEqual(['/repo/a.txt']);
  expect(outcome.rejected).toEqual(['还没有确定工作目录']);
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
