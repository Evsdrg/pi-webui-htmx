// 分支导航。树来自桥的 session.tree（Pi 的 get_tree），可分支列表来自
// session.fork_messages（Pi 的 get_fork_messages）。
//
// 两个刻意决定：
//   1. 只渲染，不缓存。树随会话变化，每次打开都重新拉；
//      桥没有为它做任何服务端状态。
//   2. 跳转用 /ui/sessions/{id}/history?leafId=...，复用已有的历史端点，
//      不新增协议面。跳转只是「查看该分支」，不改变 Pi 的当前叶子。
import type { BridgeClient } from './bridge';
import { record, text } from './stream';

const el = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

// TreeNode 对应 Pi 的 SessionTreeNode。
interface TreeNode {
  entry?: Record<string, unknown>;
  children?: TreeNode[];
  label?: string;
}

// describe 给一条条目取一行可读摘要。取不到就显示类型，不显示空行。
function describe(node: TreeNode): { kind: string; text: string } {
  const entry = record(node.entry);
  const kind = text(entry.type) || 'entry';
  const id = text(entry.id) || '';
  const message = record(entry.message);
  const role = text(message.role);
  const body = text(message.content) || text(entry.summary) || text(entry.text) || text(entry.name);
  const label = text(node.label);
  const summary = label || body || (role ? `${role} 消息` : '');
  return { kind, text: summary ? `${id ? id.slice(0, 8) + ' · ' : ''}${summary}` : id.slice(0, 8) };
}

// flatten 把树按深度优先摊平，带上层级，便于顺序渲染。
function flatten(nodes: TreeNode[], depth = 0, out: { node: TreeNode; depth: number }[] = []): { node: TreeNode; depth: number }[] {
  for (const node of nodes) {
    out.push({ node, depth });
    if (node.children?.length) flatten(node.children, depth + 1, out);
  }
  return out;
}

export class BranchNavigator {
  constructor(
    private readonly bridge: BridgeClient,
    private readonly onError: (error: unknown) => void,
    private readonly goto: (leafId: string) => void,
    private readonly fork: (entryId: string) => void,
    // sessionId 由调用方提供：桥要求 session.* 命令带会话 ID，
    // 传空串会被当成「未启动」拒绝。用函数而不是值，避免会话切换后失效。
    private readonly sessionId: () => string,
  ) {}

  async open(): Promise<void> {
    el('branch-status').textContent = '';
    await this.refresh();
  }

  async refresh(): Promise<void> {
    el('branch-status').textContent = '正在读取会话树…';
    try {
      const tree = record(await this.bridge.request<unknown>('session.tree', this.sessionId(), undefined, 30_000));
      const nodes = Array.isArray(tree.tree) ? (tree.tree as TreeNode[]) : [];
      const leafId = text(tree.leafId);
      this.renderTree(flatten(nodes), leafId);
      el('branch-status').textContent = '';
    } catch (error) {
      this.onError(error);
      el('branch-status').textContent = error instanceof Error ? error.message : '读取会话树失败';
    }
    try {
      // 注意顺序：record() 会把数组变成 {}，必须先判断数组。
      // 桥现在返回 Pi 原样的 {messages:[...]}；仍接受裸数组，
      // 避免不同桥版本下界面空白。
      const payload = await this.bridge.request<unknown>('session.fork_messages', this.sessionId(), undefined, 30_000);
      const list = Array.isArray(payload) ? payload : record(payload).messages;
      this.renderForks(Array.isArray(list) ? (list as Record<string, unknown>[]) : []);
    } catch (error) {
      this.onError(error);
    }
  }

  // renderTree 按摊平顺序渲染。分叉点用子节点数量标注，
  // 叶子给一个「查看」按钮；当前叶子标 aria-current。
  private renderTree(rows: { node: TreeNode; depth: number }[], leafId: string): void {
    const box = el('branch-tree');
    box.replaceChildren();
    if (!rows.length) {
      const empty = document.createElement('p');
      empty.className = 'branch-empty';
      empty.textContent = '此会话还没有分支结构。';
      box.append(empty);
      return;
    }
    for (const { node, depth } of rows) {
      const { kind, text: summary } = describe(node);
      const id = text(record(node.entry).id);
      const branches = node.children?.length ?? 0;
      const row = document.createElement('div');
      row.className = 'branch-node';
      row.style.paddingLeft = `${depth * 14}px`;
      const kindNode = document.createElement('span');
      kindNode.className = 'branch-kind';
      kindNode.textContent = branches > 1 ? `${kind} ⑂${branches}` : kind;
      const label = document.createElement('span');
      label.className = 'branch-text';
      label.textContent = summary;
      label.title = summary;
      row.append(kindNode, label);
      if (id) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'branch-leaf';
        button.textContent = id === leafId ? '当前' : '查看';
        button.setAttribute('aria-current', String(id === leafId));
        button.addEventListener('click', () => this.goto(id));
        row.append(button);
      }
      box.append(row);
    }
  }

  // renderForks 列出可分支的用户消息；点击直接调 session.fork。
  private renderForks(messages: Record<string, unknown>[]): void {
    const box = el('branch-forks');
    box.replaceChildren();
    if (!messages.length) {
      const empty = document.createElement('p');
      empty.className = 'branch-empty';
      empty.textContent = '没有可分支的用户消息。';
      box.append(empty);
      return;
    }
    for (const item of messages.slice(0, 200)) {
      const entryId = text(item.entryId);
      const body = text(item.text) || entryId;
      const row = document.createElement('div');
      row.className = 'branch-fork-row';
      const label = document.createElement('span');
      label.textContent = body;
      label.title = body;
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'btn';
      button.textContent = '分支';
      button.disabled = !entryId;
      button.addEventListener('click', () => this.fork(entryId));
      row.append(label, button);
      box.append(row);
    }
  }
}
