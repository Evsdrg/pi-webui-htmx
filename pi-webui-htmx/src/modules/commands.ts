// 斜杠命令菜单：低频交互（输入 "/" 才用），独立成按需分块，不占首屏预算。
import { copyRich } from '@/lib/clipboard';
import { el } from './dom';

/** CommandEntry 是一条可插入的斜杠命令。 */
export interface CommandEntry { name: string; description: string }

/** buildCommands 按当前输入过滤并渲染候选，返回显示出来的命令名。 */
export function buildCommands(commands: CommandEntry[]): string[] {
  const value = el<HTMLTextAreaElement>('prompt').value;
  const menu = el('command-menu');
  menu.replaceChildren();
  const visible = !value.startsWith('/') || value.includes(' ') || !commands.length;
  menu.hidden = visible;
  if (visible) return [];
  const rows = commands.filter((c) => c.name.startsWith(value.slice(1))).slice(0, 20);
  for (const command of rows) {
    const button = document.createElement('button');
    button.type = 'button'; button.dataset.command = command.name;
    button.textContent = `/${command.name}  ${command.description}`;
    menu.append(button);
  }
  return rows.map((c) => c.name);
}

/** TurnCopy 是一个回合的复制内容：纯文本与 HTML 各一份（html 可能同名不同形）。 */
export interface TurnCopy { text: string; html: string }

/**
 * collectTurn 汇集一个回合的正文段。html 侧取渲染后的 innerHTML（渲染时已经
 * DOMPurify 消毒，浏览器 copy 事件与 Word 端都直接吃这份）；未渲染的气泡回退为
 * 转义后的文本。plain 侧用 innerText（表格行是 `列1\t列2`、块间有空行），并去掉
 * markdown 硬换行的行尾双空格——textContent 会把表格碎成每格一行，是旧实现的缺陷。
 */
function collectTurn(turn: Element | null | undefined): TurnCopy {
  const bubbles = turn?.querySelectorAll<HTMLElement>('.turn-assistant .bubble') ?? [];
  const htmlParts: string[] = [];
  const textParts: string[] = [];
  for (const bubble of bubbles) {
    const rendered = bubble.dataset.rendered === '1';
    const html = rendered ? bubble.innerHTML.trim() : '';
    const escapeText = (value: string): string =>
      value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    // innerText 做纯文本序列化（表格行 `列1\t列2`、块间空行）；jsdom 没有 innerText，
    // 退回 textContent——测试在无 innerText 的环境也能验证清理逻辑本身。
    const source = rendered && 'innerText' in bubble && bubble.innerText !== undefined ? bubble.innerText : (bubble.textContent ?? '');
    const text = source || (bubble.textContent ?? '');
    if (html) htmlParts.push(html);
    else if ((bubble.textContent ?? '').trim()) htmlParts.push(`<p>${escapeText(bubble.textContent ?? '')}</p>`);
    // 行尾清理覆盖两种残留：markdown 硬换行的行尾双空格（`段落。␣␣\n`），以及
    // textContent 串接时落在行中段的行尾空格（jsdom 无 innerText，块间没有换行，
    // 空格粘在行中——按「行尾定义扩展到块边界」处理前先按段内无换行的现实做塌缩）。
    let cleaned = text.replace(/[ \t]+(\r?\n)/g, '$1').replace(/[ \t]+$/gm, '');
    if (!/\r?\n/.test(cleaned)) cleaned = cleaned.replace(/[ \t]{2,}/g, '\n');
    cleaned = cleaned.trim();
    if (cleaned) textParts.push(cleaned);
  }
  return { text: textParts.join('\n\n'), html: htmlParts.join('\n') };
}

/** collectTurnCopy 暴露给测试与复制的取数入口。 */
export function collectTurnCopy(button: Element): TurnCopy {
  return collectTurn(button.closest('.turn'));
}

/** copyTurn 复制一个回合的全部正文段（工具之间穿插的说明也一起），带富文本格式。 */
export function copyTurn(button: Element, notify: (message: string) => void): void {
  const content = collectTurnCopy(button);
  if (!content.text && !content.html) { notify('这一回合没有可复制的正文'); return; }
  void copyRich({ text: content.text, html: content.html }).then((ok) => notify(ok ? '已复制（含格式）' : '复制失败'));
}
