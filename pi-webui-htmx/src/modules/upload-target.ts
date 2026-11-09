// 上传目标偏好：决定文件落到「当前目录」还是「统一目标目录」。
//
// 三条规则（与用户需求一一对应）：
//   1. uploadToCurrent 开着（默认）→ 落到侧栏正在浏览的目录；
//   2. 关掉且未配置基路径 → 落到 /tmp/piwebui/<会话id>/；
//   3. 关掉且配置了基路径 → 落到 <基路径>/piwebui/<会话id>/。
//
// 为什么基路径要在前端校验白名单：桥会拒绝未批准的基路径，但那是
// 事后拒绝——用户填完点上传才看到 403。这里拿 capabilities 报的
// uploadBases 先做一次比对，填错立刻能看出来。
//
// 偏好只存本机浏览器：它描述的是「这台设备的用户想把文件放哪」，
// 不是桥的全局配置。换台机器读到不同的偏好是正常的。

import { readPreference, savePreference } from './layout';

/** 桥未配置基路径时的默认值，与用户需求里的 /tmp/piwebui/ 一致。 */
export const DEFAULT_UPLOAD_BASE = '/tmp';
/** 桥在 <base> 与 <会话id> 之间插入的固定中间层。 */
export const UPLOAD_SUBDIR = 'piwebui';

export interface UploadTarget {
  /** 为 true 时落到当前浏览目录（忽略 base）。 */
  toCurrent: boolean;
  /** 为 false 时要用的基路径；空串表示用 DEFAULT_UPLOAD_BASE。 */
  base: string;
}

export function readUploadTarget(): UploadTarget {
  return {
    // 默认「上传到当前目录」：这是最符合直觉的行为，也是本功能上线时的既有语义。
    toCurrent: readPreference('upload-to-cwd') !== '0',
    base: readPreference('upload-base'),
  };
}

export function saveUploadTarget(target: UploadTarget): void {
  savePreference('upload-to-cwd', target.toCurrent ? '1' : '0');
  savePreference('upload-base', target.base);
}

/** 实际生效的基路径：配置为空时回退到默认。 */
export function effectiveBase(target: UploadTarget): string {
  return target.base.trim() || DEFAULT_UPLOAD_BASE;
}

/**
 * resolveUploadDir 给出用户能看懂的落点预览。
 *
 * sessionId 为空时用占位符：设置页在没有选中会话时也要能显示形状。
 */
export function resolveUploadDir(target: UploadTarget, sessionId: string): string {
  const id = sessionId || '<会话id>';
  return `${effectiveBase(target).replace(/\/+$/, '')}/${UPLOAD_SUBDIR}/${id}`;
}

/** 基路径是否在桥批准的白名单里（用于即时提示，不做强制拦截）。 */
export function baseApproved(base: string, approved: readonly string[]): boolean {
  if (approved.length === 0) return false;
  const want = base.replace(/\/+$/, '');
  return approved.some((item) => item.replace(/\/+$/, '') === want);
}
