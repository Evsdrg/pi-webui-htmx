// 剪贴板助手。
//
// navigator.clipboard 只在**安全上下文**（HTTPS 或 localhost）里存在；本仓的
// 本地部署形态是 http://<ip>:<port> 访问桥，那里它是 undefined。因此不能直接
// 调 writeText，要按能力探测并退回 execCommand，最后如实返回成功与否，
// 让调用方给出可见提示（面板已经有「请手动选择」的文案）。
//
// 返回 true/false：false 表示没能写进剪贴板，调用方**必须**让用户知道，
// 而不是静默失败。
//
// copyRich 支持双格式（text/plain + text/html）：渲染后的 AI 正文贴进 Word、
// 邮件等富文本目标时保留标题/粗体/表格。两条路径都经两引擎（Chromium 141 /
// Firefox 148）实测：
//   安全上下文   —— ClipboardItem 双 flavor；
//   非安全上下文 —— copy 事件内 clipboardData.setData 双写 + execCommand('copy')
//                  触发（需要用户手势；按钮点击即是）。Firefox 对事件内
//                  setData 同样接受 text/html，所以兜底路径不分引擎。

/** RichCopy 是一次富文本复制的内容：纯文本与 HTML 各一份。 */
export interface RichCopy {
  /** 纯文本形态：贴到只收文本的目标（记事本、终端）时用。 */
  text: string;
  /** HTML 形态：必须已经过消毒（产物里的 HTML 都来自 DOMPurify 之后）。 */
  html: string;
}

/** copyText 尽力把文本写入剪贴板；返回是否成功。 */
export async function copyText(value: string): Promise<boolean> {
  const clipboard = navigator.clipboard;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(value);
      return true;
    } catch {
      // 权限被拒或非安全上下文：继续走下面的兜底。
    }
  }
  return legacyCopy(value);
}

/**
 * copyRich 尽力把双格式内容写入剪贴板；返回是否成功。
 * 两种 flavor 都给：目标按能力自取（Word/邮件吃 HTML，记事本吃纯文本）。
 */
export async function copyRich(content: RichCopy): Promise<boolean> {
  const clipboard = navigator.clipboard;
  // ClipboardItem 也要探测：Firefox 125 前没有构造器，缺了就走兜底而不是抛错。
  if (clipboard?.write && typeof ClipboardItem === 'function') {
    try {
      await clipboard.write([new ClipboardItem({
        'text/plain': new Blob([content.text], { type: 'text/plain' }),
        'text/html': new Blob([content.html], { type: 'text/html' }),
      })]);
      return true;
    } catch {
      // 权限被拒或实现差异：退回事件兜底。
    }
  }
  return legacyCopyRich(content);
}

/**
 * 一次性 copy 监听：把双格式塞进 clipboardData。必须同步完成——浏览器在
 * 事件回调返回后立刻读取数据。返回是否两条 flavor 都写上了。
 */
function writeIntoEvent(event: ClipboardEvent, content: RichCopy): boolean {
  const data = event.clipboardData;
  if (!data) return false;
  data.setData('text/plain', content.text);
  data.setData('text/html', content.html);
  event.preventDefault();
  return data.getData('text/plain') === content.text && data.getData('text/html') === content.html;
}

/** legacyCopyRich 用仿造的 copy 事件 + execCommand 兜底；非安全上下文的唯一双格式路径。 */
function legacyCopyRich(content: RichCopy): boolean {
  // 先派发一个带 DataTransfer 的合成事件给**本页监听者**？不行——合成事件不会触发
  // 浏览器的剪贴板写入。正确做法：真实 dispatch copy 事件 + execCommand 驱动，
  // 在事件回调里同步 setData。这里借用 execCommand('copy') 触发浏览器派发事件，
  // 监听器在触发前挂好。
  let ok = false;
  const onCopy = (event: ClipboardEvent): void => { ok = writeIntoEvent(event, content); };
  document.addEventListener('copy', onCopy);
  try {
    document.execCommand('copy');
  } catch {
    ok = false;
  } finally {
    document.removeEventListener('copy', onCopy);
  }
  return ok;
}

/** legacyCopy 用可选中文本框 + execCommand 兜底；用户仍可手动复制。 */
export function legacyCopy(value: string): boolean {
  const holder = document.createElement('textarea');
  holder.value = value;
  holder.setAttribute('aria-hidden', 'true');
  holder.style.position = 'fixed';
  holder.style.opacity = '0';
  document.body.append(holder);
  holder.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  holder.remove();
  return ok;
}
