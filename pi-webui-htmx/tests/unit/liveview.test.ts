import { afterEach, describe, expect, it, vi } from 'vitest';
import { LiveView } from '@/modules/stream';

// 实时正文的 markdown 渲染：真实模块会加载 marked+dompurify，测试里替换成
// 一个可预测的极简变换（只处理 **粗体**），断言的是「流式文本经过渲染器」
// 这条链路与节流语义，而不是 marked 的行为。
vi.mock('@/modules/markdown', () => ({
  safeMarkdown: async (source: string) => `<p>${source.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')}</p>`,
}));

// 实时时间线的 DOM：用户气泡 + 时间线容器 + 等待提示。
// 工作段（details.live-group）与正文段（.streaming-text）由 LiveView 交替创建。
function mount(): HTMLElement {
  const root = document.createElement('div');
  root.innerHTML = '<div id=live hidden><div id=live-user></div><div id=live-flow></div><p id=live-pending hidden></p></div>';
  document.body.append(root);
  return root;
}

// stubSyncRaf 让每帧同步执行，正文段立即可断言。
function stubSyncRaf(): void {
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => { callback(0); return 1; });
  vi.stubGlobal('cancelAnimationFrame', () => {});
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe('实时思考增量', () => {
  // U21：旧实现每个 delta 都「读整段 textContent + 拼 + 写回」，
  // 最多 40K 字符被反复复制。改为增量追加文本节点。
  it('增量追加文本节点，不做整段重写', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '甲' } });
    const el = root.querySelector('.live-think')!;
    const appendSpy = vi.spyOn(el, 'append');
    const setter = vi.spyOn(el, 'textContent', 'set');

    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '乙' } });

    expect(appendSpy).toHaveBeenCalledTimes(1);
    // 关键：不得再整段写回 textContent。
    expect(setter).not.toHaveBeenCalled();
    expect(el.textContent).toBe('甲乙');
    root.remove();
  });

  it('超过上限后停止追加', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    const big = 'x'.repeat(1000);
    for (let i = 0; i < 60; i++) view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: big } });
    const el = root.querySelector('.live-think')!;
    expect((el.textContent ?? '').length).toBe(40_000);
    root.remove();
  });

  it('begin 之后重新计数', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: 'y'.repeat(1000) } });
    view.begin();
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: 'z' } });
    const el = root.querySelector('.live-think')!;
    expect(el.textContent).toBe('z');
    root.remove();
  });
});

describe('实时过程摘要与时间线', () => {
  it('工作段运行中展开，仍显示当前动作，思考预览每 1500ms 尾沿更新', () => {
    vi.useFakeTimers();
    const root = mount();
    const view = new LiveView(root);
    view.begin('调查项目');
    view.event({ type: 'message_start' });
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '先检查目录' } });
    const group = root.querySelector<HTMLDetailsElement>('.live-group')!;
    const status = group.querySelector('.live-status')!;
    const preview = group.querySelector('.live-preview')!;
    // 运行中默认展开：工具/思考在回合进行时直接可见（ZCode 的「运行中摊开」）。
    expect(group.open).toBe(true);
    expect(status.textContent).toContain('思考中');
    expect(preview.textContent).toContain('先检查目录');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '，再读取配置' } });
    expect(preview.textContent).not.toContain('再读取配置');
    vi.advanceTimersByTime(1499);
    expect(preview.textContent).not.toContain('再读取配置');
    vi.advanceTimersByTime(1);
    expect(preview.textContent).toContain('再读取配置');
    view.event({ type: 'tool_execution_start', toolName: 'read' });
    expect(status.textContent).toContain('read');
    expect(root.querySelector('.live-tool')?.textContent).toContain('read');
    root.remove();
  });

  it('实时工具行显示命令与文件路径，长命令截断', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'c1', args: { command: 'curl -fsSL https://example.test/install.sh | sh' } });
    expect(root.querySelector('.live-tool')?.textContent).toContain('bash');
    // 运行期间必须能看到 bash 在跑什么（此前对 bash 一律返回空预览）。
    expect(root.querySelector('.live-tool')?.textContent).toContain('curl -fsSL https://example.test/install.sh');
    const long = 'x'.repeat(300);
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'c2', args: { command: long + ' --tail' } });
    const rows = root.querySelectorAll('.live-tool');
    const preview = rows[1]?.querySelector('.tool-preview')?.textContent ?? '';
    expect(preview.length).toBeLessThanOrEqual(121); // 120 + 省略号
    expect(preview.endsWith('…')).toBe(true);
    // 多行命令只取第一行进预览。
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'c3', args: { command: 'cd /tmp\nrm -rf nothing' } });
    expect(root.querySelectorAll('.live-tool')[2]?.querySelector('.tool-preview')?.textContent).toBe('cd /tmp');
    view.event({ type: 'tool_execution_start', toolName: 'read', toolCallId: 'c4', args: { path: '/workspace/src/main.go' } });
    expect(root.querySelectorAll('.live-tool')[3]?.textContent).toContain('/workspace/src/main.go');
    root.remove();
  });

  it('bash 输出随 partialResult 流式显示，按 toolCallId 归属到对应行', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'a', args: { command: 'ls' } });
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'b', args: { command: 'pwd' } });
    view.event({ type: 'tool_execution_update', toolCallId: 'a', partialResult: { content: [{ type: 'text', text: '第一行\n' }] } });
    view.event({ type: 'tool_execution_update', toolCallId: 'a', partialResult: { content: [{ type: 'text', text: '第一行\n第二行' }] } });
    view.event({ type: 'tool_execution_update', toolCallId: 'b', partialResult: { content: [{ type: 'text', text: '/workspace' }] } });
    const rows = root.querySelectorAll('.live-tool');
    // partialResult 是累积输出：直接整体替换，不逐块追加（追加会重复）。
    expect(rows[0]?.querySelector('.tool-detail')?.textContent).toBe('第一行\n第二行');
    expect(rows[1]?.querySelector('.tool-detail')?.textContent).toBe('/workspace');
    root.remove();
  });

  it('tool_execution_end 收尾：结果文本、错误配色与状态', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'a', args: { command: 'make' } });
    view.event({ type: 'tool_execution_end', toolCallId: 'a', isError: true, result: { content: [{ type: 'text', text: 'make: *** [all] Error 2' }] } });
    const row = root.querySelector<HTMLElement>('.live-tool')!;
    expect(row.dataset.state).toBe('done');
    expect(row.dataset.ok).toBe('false');
    expect(row.querySelector('.tool-detail')?.textContent).toBe('make: *** [all] Error 2');
    // 成功路径 data-ok=true（历史配色同一套选择器）。
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'b', args: { command: 'true' } });
    view.event({ type: 'tool_execution_end', toolCallId: 'b', isError: false, result: { content: [{ type: 'text', text: '' }] } });
    expect(root.querySelectorAll<HTMLElement>('.live-tool')[1]?.dataset.ok).toBe('true');
    root.remove();
  });

  it('超长 bash 输出只保留尾部并标明省略', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin();
    view.event({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'a', args: { command: 'cat big' } });
    const huge = 'A'.repeat(30_000) + 'TAIL';
    view.event({ type: 'tool_execution_update', toolCallId: 'a', partialResult: { content: [{ type: 'text', text: huge }] } });
    const body = root.querySelector('.tool-detail')?.textContent ?? '';
    expect(body.length).toBeLessThan(25_000);
    expect(body).toContain('TAIL');
    expect(body).toContain('已省略');
    root.remove();
  });

  it('正文段落在工作段之间，而不是堆到末尾', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('任务');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'thinking_delta', delta: '先想' } });
    view.event({ type: 'tool_execution_start', toolName: 'read' });
    view.event({ type: 'message_start' });
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '正文A' } });
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '，继续' } });
    view.event({ type: 'tool_execution_start', toolName: 'edit' });
    const flow = root.querySelector('#live-flow')!;
    const groups = flow.querySelectorAll<HTMLElement>('.live-group');
    // 工作段进行中：活动信号只挂在最新一段，旧段不再扫光。
    expect(groups[0]!.hasAttribute('data-active')).toBe(false);
    expect(groups[1]!.hasAttribute('data-active')).toBe(true);
    view.event({ type: 'message_start' });
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '正文B' } });

    const kinds = [...flow.children].map((node) => (node.tagName === 'DETAILS' ? 'work' : 'text'));
    expect(kinds).toEqual(['work', 'text', 'work', 'text']);
    const texts = [...flow.querySelectorAll('.streaming-text')].map((node) => node.textContent);
    expect(texts).toEqual(['正文A，继续', '正文B']);
    // 各工作段只含自己的工具：read 在第一段、edit 在第二段。
    expect(groups[0]!.textContent).toContain('read');
    expect(groups[0]!.textContent).not.toContain('edit');
    expect(groups[1]!.textContent).toContain('edit');
    // 正文开始流式后活动信号交给光标：最新的工作段也停止扫光。
    expect(groups[1]!.hasAttribute('data-active')).toBe(false);
    root.remove();
  });

  it('回合结算时收起运行中展开的工作段', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('任务');
    view.event({ type: 'tool_execution_start', toolName: 'read' });
    const group = root.querySelector<HTMLDetailsElement>('.live-group')!;
    expect(group.open).toBe(true);
    view.finish();
    // 结算：收起，避免与随后折叠的历史过程组之间闪高度差。
    expect(group.open).toBe(false);
    root.remove();
  });

  // 用户实报：流式期间正文显示为原始 markdown（`**粗体**`、表格语法），
  // 要等结算重读历史才正常。实时段必须也走 markdown 渲染（节流）。
  it('流式正文按节流渲染 markdown，未达节流间隔前保持纯文本', async () => {
    vi.useFakeTimers();
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('问题');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '**重点**' } });
    const seg = root.querySelector<HTMLElement>('.streaming-text')!;
    // 节流窗口内：纯文本撑着（还没到渲染时刻）。
    expect(seg.textContent).toBe('**重点**');
    await vi.advanceTimersByTimeAsync(349);
    expect(seg.querySelector('strong')).toBeNull();
    await vi.advanceTimersByTimeAsync(1);
    // 渲染后：markdown 生效，且不再含原始标记。
    expect(seg.querySelector('strong')?.textContent).toBe('重点');
    expect(seg.textContent).not.toContain('**');
    root.remove();
  });

  it('超长正文段不做实时渲染，保持纯文本', async () => {
    vi.useFakeTimers();
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('问题');
    const big = 'x'.repeat(61_000);
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: big } });
    await vi.advanceTimersByTimeAsync(500);
    const seg = root.querySelector<HTMLElement>('.streaming-text')!;
    expect(seg.querySelector('p')).toBeNull();
    expect(seg.textContent?.length).toBe(big.length);
    root.remove();
  });

  it('开始处理前显示等待提示，第一段内容出现后隐藏', () => {
    const root = mount();
    const view = new LiveView(root);
    view.begin('问题');
    const pending = root.querySelector<HTMLElement>('#live-pending')!;
    expect(pending.hidden).toBe(false);
    expect(pending.textContent).toContain('正在处理');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '回复' } });
    expect(pending.hidden).toBe(true);
    root.remove();
  });

  it('纯空白与丢弃标记的正文段保持隐藏，出现真内容才显示', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('问题');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '\n\n' } });
    const seg = root.querySelector<HTMLElement>('.streaming-text')!;
    expect(seg.hidden).toBe(true);
    // 提供商把文本丢弃后的占位标记也不该显示成气泡。
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '[dropped ]' } });
    expect(seg.hidden).toBe(true);
    expect(seg.textContent).not.toContain('[dropped');
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '真内容' } });
    expect(seg.hidden).toBe(false);
    expect(seg.textContent).toBe('真内容');
    root.remove();
  });

  // 用户实报：AI 阶段小结并继续任务时，时间线上多出一条「空白用户消息」。
  // 路径是 settled 的 clear() 之后又来了一帧增量（排队消息续跑 / 重连补发）：
  // clear() 只清用户气泡的文字、没复位 hidden，增量把实时层重新显示时，
  // 一个空的用户气泡就露出来了。
  it('clear 之后新的增量不会露出空白用户气泡', () => {
    stubSyncRaf();
    const root = mount();
    const view = new LiveView(root);
    view.begin('消息');
    const user = root.querySelector<HTMLElement>('#live-user')!;
    expect(user.hidden).toBe(false);
    view.clear();
    // settled 清理后，新一轮的增量到来（没有经过 begin）。
    view.event({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '续跑' } });
    expect(user.hidden).toBe(true);
    expect(user.textContent).toBe('');
    // 新一轮的真实内容仍在。
    expect(root.querySelector('.streaming-text')?.textContent).toContain('续跑');
    root.remove();
  });
});
