
// ---- 无预览工具现在也有一行可读摘要 ----
//
// 此前 ctx_* / todowrite / codemode 的 toolPreview 一律返回空串，
// 历史与实时都渲染成「只有工具名的空行」。实测这些占全部调用的 2.8%
// （ctx_* 1,970 次、todowrite 1,153 次），连续七八行就是刷屏。
import { expect, test } from 'vitest';
import { toolPreview } from '@/modules/stream';

test('ctx_reduce 预览显示 tag 列表', () => {
  expect(toolPreview('ctx_reduce', { drop: [12, 15, 18] })).toBe('§12 §15 §18');
  expect(toolPreview('ctx_reduce', { drop: [1, 2, 3, 4] })).toBe('§1 §2 §3 …');
  expect(toolPreview('ctx_reduce', {})).toBe('');
});

test('ctx_memory 与 ctx_note 预览显示动作与内容', () => {
  expect(toolPreview('ctx_memory', { action: 'pin', content: '构建前先清空 feeds' })).toBe('pin 构建前先清空 feeds');
  expect(toolPreview('ctx_memory', { action: 'pin' })).toBe('pin');
  expect(toolPreview('ctx_note', { action: 'add', content: '待复查' })).toBe('add 待复查');
  expect(toolPreview('ctx_memory', {})).toBe('');
});

test('ctx_search 预览显示查询词', () => {
  expect(toolPreview('ctx_search', { query: '构建顺序' })).toBe('构建顺序');
});

test('ctx_expand 预览显示编号或区间', () => {
  expect(toolPreview('ctx_expand', { tag: 42 })).toBe('§42');
  expect(toolPreview('ctx_expand', { start: 10, end: 20 })).toBe('§10-§20');
  expect(toolPreview('ctx_expand', {})).toBe('');
});

test('todowrite 预览显示任务计数', () => {
  const args = { todos: [
    { content: 'a', status: 'completed' },
    { content: 'b', status: 'in_progress' },
    { content: 'c', status: 'pending' },
  ] };
  expect(toolPreview('todowrite', args)).toBe('3 项 · 1 进行中 · 1 已完成');
  expect(toolPreview('todowrite', { todos: [] })).toBe('');
});

test('codemode 预览显示脚本调用的工具', () => {
  const code = `const r = await Promise.allSettled([tools.grep({path:'/a'}), tools.ls({path:'/b'})]);`;
  expect(toolPreview('codemode', { code })).toBe('grep, ls · 2 个工具');
  expect(toolPreview('codemode', { code: 'await Promise.all([]);' })).toBe('脚本');
  expect(toolPreview('codemode', {})).toBe('');
});

test('未知工具仍然没有预览（不猜内容）', () => {
  expect(toolPreview('some_unknown_tool', { whatever: 1 })).toBe('');
});
