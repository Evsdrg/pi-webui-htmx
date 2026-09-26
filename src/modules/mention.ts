// @ 文件补全。输入 @ 后弹出候选，选中后把相对路径插入输入框。
//
// 三个刻意的决定：
//   1. 查询走桥的 files.index，由服务端排序。@ 菜单每敲一个字都请求一次，
//      把几千条原文拉回浏览器再过滤等于每次都传上百 KB。
//   2. 输入防抖 120ms。手工验证时低于这个值会 seen 重复键，
//      高于则明显跟不上打字。
//   3. 只认「光标前最近的一个 @」，且 @ 后不能有空格——否则
//      "email me @ 3pm" 这种正常句子也会弹菜单。

// strings_TrimLeft 只去行首空白；用正则而不是 trim 是为了不动尾部。
const strings_TrimLeft = (v: string) => v.replace(/^\s+/, '');

const DEBOUNCE_MS = 120;
const MAX_VISIBLE = 20;

export interface FileMatch { path: string }

interface State {
  active: boolean;
  /** @ 在输入框中的起始下标 */
  start: number;
  query: string;
  items: string[];
  selected: number;
}

const closed: State = { active: false, start: 0, query: '', items: [], selected: 0 };

// detect 从光标处往前找最近一个未被空格隔开的 @。
// 返回 null 表示当前不该弹菜单。
export function detect(value: string, caret: number): { start: number; query: string } | null {
  if (caret <= 0) return null;
  const head = value.slice(0, caret);
  const at = head.lastIndexOf('@');
  if (at < 0) return null;
  // @ 前一个字符是空白时才认为是触发符；夹在单词中间（a@b）不算。
  if (at > 0 && !/\s/.test(head[at - 1] ?? '')) return null;
  const query = head.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { start: at, query };
}

// apply 把选中的路径替换掉 @query 那一段，返回新值与新的光标位置。
//
// at 由 detect 给出，指向 @ 本身。这里仍然防御性地校正：
// 调用方传错下标时，往前找最近的一个 @，而不是拼出 "@a@b" 这种坏文本。
export function apply(value: string, at: number, path: string): { value: string; caret: number } {
  let start = at;
  if (value[start] !== '@') {
    const found = value.lastIndexOf('@', Math.max(0, at));
    start = found >= 0 ? found : at;
  }
  const head = value.slice(0, start);
  const rest = value.slice(start);
  const tail = rest.startsWith('@') ? rest.replace(/^@[^\s]*/, '') : rest;
  // 原 query 后面可能已有空格；不先去掉就会拼出两个空格。
  const trimmed = strings_TrimLeft(tail);
  const inserted = `${head}@${path} `;
  return { value: inserted + trimmed, caret: inserted.length };
}

export class FileCompleter {
  private state: State = { ...closed };
  private timer: number | undefined;
  private seq = 0;
  // 菜单元素缺失时自我禁用。缺元素还继续跑会在每次输入时抛异常，
  // 把同一个处理器里后面的 updateControls 一起带崩——发送按钮状态、
  // 排队提示全都不更新，而表面上看只是「@ 补全没反应」。
  private readonly disabled: boolean;

  constructor(
    private readonly input: HTMLTextAreaElement,
    private readonly menu: HTMLElement | null,
    private readonly cwd: () => string,
    private readonly search: (cwd: string, query: string) => Promise<FileMatch[]>,
  ) { this.disabled = menu === null; }

  get active(): boolean { return !this.disabled && this.state.active; }

  // refresh 在每次输入/光标移动后调用；内部自行判断该不该显示。
  refresh(): void {
    if (this.disabled) return;
    const found = detect(this.input.value, this.input.selectionStart ?? this.input.value.length);
    window.clearTimeout(this.timer);
    if (!found) { this.hide(); return; }
    this.state = { active: true, start: found.start, query: found.query, items: [], selected: 0 };
    this.render();
    this.timer = window.setTimeout(() => void this.load(found.query), DEBOUNCE_MS);
  }

  hide(): void {
    if (this.disabled || this.menu === null) return;
    window.clearTimeout(this.timer);
    this.state = { ...closed };
    this.menu.replaceChildren();
    this.menu.hidden = true;
  }

  private async load(query: string): Promise<void> {
    const mine = ++this.seq;
    const cwd = this.cwd();
    if (!cwd) return;
    let items: string[] = [];
    try {
      const matches = await this.search(cwd, query);
      items = matches.map((m) => m.path);
    } catch {
      // 补全失败不该打断输入：静默保持空菜单即可。
      items = [];
    }
    // 防抖期间用户又输入了，丢弃这次结果。
    if (mine !== this.seq || !this.state.active) return;
    this.state.items = items.slice(0, MAX_VISIBLE);
    this.state.selected = 0;
    this.render();
  }

  // move 上下移动选择；返回 true 表示已消费该按键，调用方应 preventDefault。
  move(delta: number): boolean {
    if (this.disabled || !this.state.active || this.state.items.length === 0) return false;
    const n = this.state.items.length;
    this.state.selected = (this.state.selected + delta + n) % n;
    this.render();
    return true;
  }

  // choose 插入当前选中项；没有选中项时返回 false。
  //
  // 调用方必须把 false 也当作「已消费按键」：菜单开着时按 Enter 若放行，
  // 表单会把 "@" 这种半个查询当成消息发出去。要发消息请先 Esc 关菜单。
  choose(): boolean {
    if (this.disabled || !this.state.active) return false;
    const path = this.state.items[this.state.selected];
    if (!path) return false;
    const { value, caret } = apply(this.input.value, this.state.start, path);
    this.input.value = value;
    this.input.setSelectionRange(caret, caret);
    this.hide();
    return true;
  }

  private render(): void {
    const menu = this.menu;
    if (menu === null) return;
    menu.replaceChildren();
    if (this.state.items.length === 0) {
      // 有 @ 但没有候选时不弹空框，避免遮挡输入区。
      menu.hidden = true;
      return;
    }
    menu.hidden = false;
    this.state.items.forEach((path, index) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'mention-item';
      button.dataset.path = path;
      button.setAttribute('aria-selected', String(index === this.state.selected));
      const label = document.createElement('span');
      label.className = 'mention-path';
      label.textContent = path;
      button.append(label);
      menu.append(button);
    });
  }
}
