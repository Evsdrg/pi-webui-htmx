// 斜杠命令菜单：低频交互（输入 "/" 才用），独立成按需分块，不占首屏预算。
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

/** copyTurn 复制一个回合的全部正文段（工具之间穿插的说明也一起）。 */
export function copyTurn(button: Element, notify: (message: string) => void): void {
  const turn = button.closest('.turn');
  const parts = [...(turn?.querySelectorAll('.turn-assistant .bubble') ?? [])].map((node) => node.textContent ?? '').filter(Boolean);
  void navigator.clipboard.writeText(parts.join('\n\n')).then(() => notify('已复制')).catch(() => notify('复制失败'));
}
