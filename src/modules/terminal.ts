// 模块只在用户启动终端时导入，xterm 与 CSS 不进入首屏。
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import type { BridgeClient } from './bridge';

export class TerminalView {
  private terminal: Terminal | undefined;
  private addon: FitAddon | undefined;
  private observer: ResizeObserver | undefined;
  private resizeTimer: ReturnType<typeof setTimeout> | undefined;
  private id = '';
  private opening = false;
  private early: { id: string; text: string }[] = [];
  private queued = 0;
  private generation = 0;
  private input = '';
  private inputBusy = false;
  private inputTimer: ReturnType<typeof setTimeout> | undefined;
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {}

  async open(cwd: string): Promise<void> {
    if (this.id || this.opening) return;
    this.opening = true; const generation = ++this.generation;
    const mount = document.getElementById('terminal-mount')!; mount.replaceChildren();
    this.terminal = new Terminal({ cursorBlink: true, fontSize: 12, fontFamily: 'ui-monospace, monospace', scrollback: 1000, theme: { background: '#181818', foreground: '#ededed' }, allowProposedApi: false });
    this.addon = new FitAddon(); this.terminal.loadAddon(this.addon); this.terminal.open(mount); this.addon.fit();
    try {
      const info = await this.bridge.request<{terminalId:string}>('terminal.open', '', { cwd, cols: this.terminal.cols, rows: this.terminal.rows });
      if (generation !== this.generation) { await this.bridge.request('terminal.close', '', { terminalId: info.terminalId }); return; }
      this.id = info.terminalId; document.getElementById('terminal-notice')!.textContent = `终端已连接 · ${cwd}`;
      this.terminal.onData((data) => this.enqueueInput(data));
      this.observer = new ResizeObserver(() => { clearTimeout(this.resizeTimer); this.resizeTimer = setTimeout(() => this.fit(), 100); }); this.observer.observe(mount);
      for (const frame of this.early) if (frame.id === this.id) this.output(frame.text);
      this.early = []; this.terminal.focus();
    } catch (error) { this.release(); throw error; } finally { this.opening = false; }
  }
  fit(): void {
    if (!this.terminal || !this.addon || !this.id || document.getElementById('panel-terminal')?.hidden) return;
    this.addon.fit();
    void this.bridge.request('terminal.resize', '', { terminalId: this.id, cols: this.terminal.cols, rows: this.terminal.rows }).catch(this.onError);
  }
  event(event: string, data: Record<string, unknown>): void {
    if (event === 'terminal.output' && typeof data.data === 'string' && typeof data.terminalId === 'string') {
      if (data.terminalId === this.id) this.output(data.data);
      else if (this.opening && this.early.length < 32 && this.early.reduce((n, f) => n + f.text.length, 0) < 64_000) this.early.push({ id: data.terminalId, text: data.data.slice(0, 8192) });
    }
    if (event === 'bridge.terminal_closed' && data.terminalId === this.id) { this.release(); document.getElementById('terminal-notice')!.textContent = '终端已退出。'; }
  }
  private enqueueInput(data: string): void {
    if (!this.id || !this.bridge.connected) return;
    if (new TextEncoder().encode(this.input + data).length > 60 * 1024) { this.pauseInput(new Error('终端输入超过缓冲上限，输入已暂停；请关闭后重新连接。')); return; }
    this.input += data;
    if (!this.inputBusy && !this.inputTimer) this.inputTimer = setTimeout(() => { this.inputTimer = undefined; void this.flushInput(); }, 10);
  }
  private async flushInput(): Promise<void> {
    if (!this.input || !this.id || !this.bridge.connected || this.inputBusy) return;
    const data = this.input; const generation = this.generation; this.input = ''; this.inputBusy = true;
    try { await this.bridge.request('terminal.input', '', { terminalId: this.id, data }); }
    catch (error) { if (generation === this.generation) this.pauseInput(error); }
    finally { if (generation === this.generation) { this.inputBusy = false; if (this.input) void this.flushInput(); } }
  }
  private pauseInput(error: unknown): void {
    this.input = ''; clearTimeout(this.inputTimer); this.inputTimer = undefined;
    if (this.terminal) this.terminal.options.disableStdin = true;
    this.onError(error);
  }
  private output(data: string): void {
    if (!this.terminal) return;
    if (this.queued + data.length > 1024 * 1024) { this.onError(new Error('终端输出超过浏览器缓冲上限，已关闭终端。')); void this.close().catch(this.onError); return; }
    this.queued += data.length; this.terminal.write(data, () => { this.queued = Math.max(0, this.queued - data.length); });
  }
  async close(): Promise<void> {
    const id = this.id;
    // 先得到服务端关闭确认；断线时保留 ID，不宣称资源已释放。
    if (id && !this.bridge.connected) throw new Error('连接已断开，无法确认终端关闭；请重连后重试。');
    if (id) await this.bridge.request('terminal.close', '', { terminalId: id });
    this.generation++;
    this.release(); document.getElementById('terminal-notice')!.textContent = '终端已关闭，资源已释放。';
  }
  disconnected(): void {
    this.input = ''; clearTimeout(this.inputTimer); this.inputTimer = undefined;
    if (this.terminal) this.terminal.options.disableStdin = true;
    document.getElementById('terminal-notice')!.textContent = '连接中断，终端输入已暂停；重连后可关闭并重新打开。';
  }
  private release(): void { this.generation++; clearTimeout(this.inputTimer); this.inputTimer = undefined; this.input = ''; this.inputBusy = false; clearTimeout(this.resizeTimer); this.observer?.disconnect(); this.observer = undefined; this.terminal?.dispose(); this.terminal = undefined; this.addon = undefined; this.id = ''; this.early = []; this.queued = 0; }
  dispose(): void { const id = this.id; this.generation++; if (id && this.bridge.connected) void this.bridge.request('terminal.close', '', { terminalId: id }).catch(() => {}); this.release(); }
}
