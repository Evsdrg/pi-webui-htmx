import { afterEach, expect, it, vi } from 'vitest';
import { EventCursor, LiveView, messageText, runStateAfter } from '../../src/modules/stream';
import { clampSidebar, readDraft, saveDraft, applyTheme } from '../../src/modules/layout';

afterEach(() => { localStorage.clear(); document.body.replaceChildren(); vi.unstubAllGlobals(); });
it('agent_end 不提前结束运行，settled 才结束', () => { expect(runStateAfter('running',{type:'agent_end'})).toBe('running'); expect(runStateAfter('running',{type:'agent_settled'})).toBe('idle'); expect(runStateAfter('running',{type:'auto_retry_start'})).toBe('retrying'); });
it('只有需要回答的扩展事件进入等待状态', () => { for (const method of ['notify','setStatus','setWidget']) expect(runStateAfter('running',{type:'extension_ui_request',method})).toBe('running'); for (const method of ['select','confirm','input','editor']) expect(runStateAfter('running',{type:'extension_ui_request',method})).toBe('waiting_input'); });
it('补发事件去重，且事件流不能自己切换 epoch', () => {
  const cursor = new EventCursor();
  expect(cursor.accept('a',4)).toBe(true);
  expect(cursor.accept('a',4)).toBe(false);
  expect(cursor.accept('a',3)).toBe(false);
  // 事件流里冒出另一个 epoch，视为上一个 worker 的延迟帧：丢弃，不切游标（U16）。
  // 旧实现遇到新 epoch 就把 seq 归零并接收，旧帧会被当成新事件应用。
  expect(cursor.accept('b',1)).toBe(false);
  expect(cursor.epoch).toBe('a');
  expect(cursor.seq).toBe(4);
  expect(cursor.accept('b',NaN)).toBe(false);
  // 切换 epoch 只走订阅确认里的权威值。
  cursor.begin('b',7);
  expect(cursor.epoch).toBe('b');
  expect(cursor.seq).toBe(7);
  expect(cursor.accept('b',8)).toBe(true);
  expect(cursor.accept('a',99)).toBe(false);
  cursor.reset(); expect(cursor.seq).toBe(0);
});
it('消息正文只读取文本块', () => { expect(messageText({content:[{type:'text',text:'正文'},{type:'toolCall',text:'不显示'}]})).toBe('正文'); expect(messageText({content:'用户文字'})).toBe('用户文字'); });
it('草稿按会话隔离，数量和大小均有上限', () => { for (let i=0;i<12;i++) saveDraft(String(i),'x'.repeat(30_000)); expect(readDraft('0')).toBe(''); expect(readDraft('11')).toHaveLength(20_000); expect(JSON.parse(localStorage.getItem('pi-ui:drafts')!)).toHaveLength(8); saveDraft('11',''); expect(readDraft('11')).toBe(''); });
it('损坏草稿与无效主题安全回退', () => { localStorage.setItem('pi-ui:drafts','{'); expect(readDraft('x')).toBe(''); applyTheme('unknown'); expect(document.documentElement.dataset.theme).toBeUndefined(); });
it('侧栏宽度钳制且拒绝非数值', () => { expect(clampSidebar(999)).toBe(380); expect(clampSidebar(1)).toBe(200); expect(clampSidebar(NaN)).toBe(260); });
it('大量增量仅使用一个文本节点，正文按文本处理', () => {
  vi.stubGlobal('requestAnimationFrame',vi.fn(() => 1)); vi.stubGlobal('cancelAnimationFrame',vi.fn());
  document.body.innerHTML = '<div id=live><div id=live-text></div><div id=live-user></div></div>';
  const live = new LiveView(document.getElementById('live')!); live.begin('用户');
  for (let i=0;i<1000;i++) live.event({type:'message_update',assistantMessageEvent:{type:'text_delta',delta:'<b>x</b>'}});
  live.finish(); const text = document.getElementById('live-text')!; expect(text.childNodes).toHaveLength(1); expect(text.querySelector('b')).toBeNull();
  live.clear(); expect(text.textContent).toBe(''); expect(document.getElementById('live')!.hidden).toBe(true);
});
