// 上传目标偏好的读写与落点预览。
// 三条规则的语义在这里钉死：默认上传到当前目录；关掉后回退到 /tmp；
// 配置了基路径则用基路径。落点恒为 <base>/piwebui/<会话id>。
import { afterEach, expect, test, vi } from 'vitest';
import {
  DEFAULT_UPLOAD_BASE,
  baseApproved,
  effectiveBase,
  readUploadTarget,
  resolveUploadDir,
  saveUploadTarget,
} from '@/modules/upload-target';

afterEach(() => { localStorage.clear(); vi.unstubAllGlobals(); });

const SESSION = '01a1ba81-1c77-72b4-bf0c-31c986eeab7b';

test('默认上传到当前目录', () => {
  expect(readUploadTarget().toCurrent).toBe(true);
});

test('关掉后默认基路径是 /tmp', () => {
  saveUploadTarget({ toCurrent: false, base: '' });
  const target = readUploadTarget();
  expect(target.toCurrent).toBe(false);
  expect(effectiveBase(target)).toBe(DEFAULT_UPLOAD_BASE);
});

test('保存的基路径会被读回', () => {
  saveUploadTarget({ toCurrent: false, base: '/scratch' });
  expect(readUploadTarget()).toEqual({ toCurrent: false, base: '/scratch' });
});

test('落点预览带 piwebui 中间层与会话 id', () => {
  expect(resolveUploadDir({ toCurrent: false, base: '/scratch' }, SESSION)).toBe(`/scratch/piwebui/${SESSION}`);
});

test('基路径末尾的斜杠不会变成双斜杠', () => {
  expect(resolveUploadDir({ toCurrent: false, base: '/scratch/' }, SESSION)).toBe(`/scratch/piwebui/${SESSION}`);
});

test('没有会话时用占位符，设置页仍能显示形状', () => {
  expect(resolveUploadDir({ toCurrent: false, base: '/tmp' }, '')).toBe('/tmp/piwebui/<会话id>');
});

test('白名单比对忽略末尾斜杠', () => {
  expect(baseApproved('/scratch/', ['/tmp', '/scratch'])).toBe(true);
  expect(baseApproved('/etc', ['/tmp', '/scratch'])).toBe(false);
  // 空白名单说明桥没配置任何基路径：不能把用户的任何输入判成「已批准」。
  expect(baseApproved('/tmp', [])).toBe(false);
});
