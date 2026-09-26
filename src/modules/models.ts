// 模型配置编辑器。凭据只留在桥所在的机器，页面永远拿不到真实密钥：
// 读回来时已被桥打码成 "***"，保存时由桥按磁盘真值还原。
import type { BridgeClient } from './bridge';
import { record, text } from './stream';

const el = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;

// parseHeaders 把「每行 name: value」转成对象。
// 拒绝空名与含控制字符的名/值——否则能注入请求头。
function parseHeaders(raw: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of raw.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const at = trimmed.indexOf(':');
    if (at <= 0) throw new Error('自定义头部格式应为「名称: 值」');
    const name = trimmed.slice(0, at).trim();
    const value = trimmed.slice(at + 1).trim();
    if (/[\r\n]/.test(name) || /[\r\n]/.test(value)) throw new Error('头部不能包含换行');
    out[name] = value;
  }
  return out;
}

function credential(): { baseUrl: string; api: string; apiKey: string; headers: Record<string, string> } {
  return {
    baseUrl: el<HTMLInputElement>('discover-url').value.trim(),
    api: el<HTMLInputElement>('discover-api').value.trim(),
    apiKey: el<HTMLInputElement>('discover-key').value,
    headers: parseHeaders(el<HTMLTextAreaElement>('discover-headers').value),
  };
}

export class ModelsEditor {
  private loading = false;
  constructor(private readonly bridge: BridgeClient, private readonly onError: (error: unknown) => void) {}

  async open(): Promise<void> {
    el('models-status').textContent = '';
    await this.reload();
  }

  async reload(): Promise<void> {
    if (this.loading) return;
    this.loading = true;
    try {
      const raw = await this.bridge.request<unknown>('config.models.raw', '', undefined, 30_000);
      el<HTMLTextAreaElement>('models-editor').value = JSON.stringify(raw, null, 2);
      el('models-status').textContent = '';
    } catch (error) {
      this.onError(error);
      el('models-status').textContent = error instanceof Error ? error.message : '读取配置失败';
    } finally {
      this.loading = false;
    }
  }

  async save(): Promise<void> {
    const editor = el<HTMLTextAreaElement>('models-editor');
    let doc: unknown;
    try {
      doc = JSON.parse(editor.value);
    } catch (error) {
      el('models-status').textContent = `不是合法 JSON：${error instanceof Error ? error.message : '解析失败'}`;
      return;
    }
    if (typeof doc !== 'object' || doc === null || Array.isArray(doc)) {
      el('models-status').textContent = '配置必须是 JSON 对象';
      return;
    }
    el('models-status').textContent = '正在保存…';
    try {
      await this.bridge.request('config.models.write', '', { config: doc }, 30_000);
      el('models-status').textContent = '';
      // 保存后重新读取：桥会把 "***" 还原成真值，界面必须刷新成还原后的
      // 文档，否则用户接着编辑时看到的仍是旧快照。
      await this.reload();
    } catch (error) {
      this.onError(error);
      el('models-status').textContent = error instanceof Error ? error.message : '保存失败';
    }
  }

  async discover(): Promise<void> {
    const result = el('discover-result');
    result.textContent = '正在向供应商查询…';
    try {
      const { baseUrl, api, apiKey, headers } = credential();
      if (!baseUrl) throw new Error('请先填写 baseURL');
      const data = await this.bridge.request<unknown>('config.models.discover', '', { baseUrl, api, apiKey, headers }, 30_000);
      this.renderResult('供应商返回的模型', record(data).models);
    } catch (error) {
      this.onError(error);
      result.textContent = error instanceof Error ? error.message : '发现失败';
    }
  }

  async test(): Promise<void> {
    const result = el('discover-result');
    result.textContent = '正在测试连通…';
    try {
      const { baseUrl, api, apiKey, headers } = credential();
      if (!baseUrl) throw new Error('请先填写 baseURL');
      const data = record(await this.bridge.request<unknown>('config.models.test', '', { baseUrl, api, apiKey, headers }, 30_000));
      result.textContent = text(data.message) || (data.ok ? '连通正常' : '连通失败');
    } catch (error) {
      this.onError(error);
      result.textContent = error instanceof Error ? error.message : '测试失败';
    }
  }

  // renderResult 只展示供应商返回的 ID 与名称，不渲染任何 HTML。
  private renderResult(title: string, models: unknown): void {
    const rows = Array.isArray(models) ? models : [];
    const box = el('discover-result');
    box.replaceChildren();
    const heading = document.createElement('strong');
    heading.textContent = `${title}（${rows.length}）`;
    box.append(heading);
    const list = document.createElement('ul');
    list.className = 'discover-list';
    for (const value of rows.slice(0, 200)) {
      const item = record(value);
      const row = document.createElement('li');
      const id = text(item.id) || text(item.modelId);
      const name = text(item.name) || id;
      row.textContent = id ? `${name} · ${id}` : name || JSON.stringify(value).slice(0, 120);
      list.append(row);
    }
    box.append(list);
    if (!rows.length) box.append(document.createTextNode('供应商没有返回模型。'));
  }
}
