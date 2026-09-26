// 终端：xterm.js。数据面走 WS，不走 htmx——PTY 是字节流，没有 HTML 表示。
// xterm 约 80 KB，同样只在真正打开终端时加载。

type Terminal = {
  open(container: HTMLElement): void;
  write(data: string): void;
  loadAddon(addon: unknown): void;
  onData(cb: (data: string) => void): void;
  onResize(cb: (size: { cols: number; rows: number }) => void): void;
  dispose(): void;
};
type FitAddon = { fit(): void };
type TerminalCtor = new (options: { cursorBlink: boolean; fontSize: number; convertEol: boolean }) => Terminal;
type FitCtor = new () => FitAddon;

interface Handle {
  term: Terminal;
  fit: FitAddon;
}

const terms = new Map<string, Handle>();

export interface TerminalCallbacks {
  onInput(data: string): void;
  onResize(cols: number, rows: number): void;
}

let loading: Promise<{ Terminal: TerminalCtor; FitAddon: FitCtor }> | null = null;

function load(): Promise<{ Terminal: TerminalCtor; FitAddon: FitCtor }> {
  loading ??= (async () => {
    const [xterm, fit] = await Promise.all([
      import(/* @vite-ignore */ "@xterm/xterm"),
      import(/* @vite-ignore */ "@xterm/addon-fit"),
      import(/* @vite-ignore */ "@xterm/xterm/css/xterm.css"),
    ]);
    return { Terminal: xterm.Terminal as unknown as TerminalCtor, FitAddon: fit.FitAddon as unknown as FitCtor };
  })();
  return loading;
}

/** 在容器内创建终端。重复调用同一 id 会直接返回。 */
export function openTerminal(
  terminalId: string,
  container: HTMLElement,
  cb: TerminalCallbacks,
): void {
  if (terms.has(terminalId)) return;
  void load().then(({ Terminal: Term, FitAddon: Fit }) => {
    if (terms.has(terminalId)) return;
    const term = new Term({ cursorBlink: true, fontSize: 13, convertEol: true });
    const fit = new Fit();
    term.loadAddon(fit);
    term.open(container);
    fit.fit();
    term.onData(cb.onInput);
    term.onResize((size) => cb.onResize(size.cols, size.rows));
    terms.set(terminalId, { term, fit });
  });
}
