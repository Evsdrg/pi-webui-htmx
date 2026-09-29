// 模型配置编辑器。凭据只留在桥所在的机器，页面永远拿不到真实密钥：
// 读回来时已被桥打码成 "***"，保存时由桥按磁盘真值还原。
//
// 结构对齐 Pi Web 的 ModelsConfig：左侧 provider → model 两级树，
// 右侧是选中项的字段表单；JSON 源码保留为逃生门，表单覆盖不到的
// 自定义字段（compat、thinkingLevelMap 的细节等）仍可手改。
import type { BridgeClient } from './bridge';
import { record, text } from './stream';
import { el } from './dom';

// 文档形状。providers 是唯一顶层键，其余自定义键原样保留——
// 保存时整份写回，所以这里只读不写的字段不会丢。
type ModelsDoc = { providers?: Record<string, ProviderEntry> } & Record<string, unknown>;
interface ProviderEntry {
  api?: string;
  baseUrl?: string;
  apiKey?: string;
  headers?: Record<string, string>;
  models?: ModelEntry[];
  [key: string]: unknown;
}
interface ModelEntry {
  id?: string;
  name?: string;
  reasoning?: boolean;
  input?: string[];
  contextWindow?: number;
  maxTokens?: number;
  thinkingLevelMap?: Record<string, string | null>;
  [key: string]: unknown;
}

/** 当前选中的树节点。provider 与 model 都可能是 null（未选中）。 */
interface Selection {
  provider: string | null;
  /** 模型在 providers[provider].models 里的下标；null 表示选中的是 provider 本身。 */
  model: number | null;
}

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

function formatHeaders(headers: unknown): string {
  const map = record(headers);
  return Object.entries(map).map(([name, value]) => `${name}: ${text(value) ?? String(value)}`).join('\n');
}

// 思考等级的固定顺序。Pi Web 用同一组（THINKING_LEVELS），
// 顺序固定才能让映射表的行不会每次刷新都换位置。
const THINKING_LEVELS = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const;

export class ModelsEditor {
  private loading = false;
  private doc: ModelsDoc = {};
  private selection: Selection = { provider: null, model: null };
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
      this.doc = (record(raw) as ModelsDoc) ?? {};
      el<HTMLTextAreaElement>('models-editor').value = JSON.stringify(this.doc, null, 2);
      el('models-status').textContent = '';
      this.renderTree();
    } catch (error) {
      this.onError(error);
      el('models-status').textContent = error instanceof Error ? error.message : '读取配置失败';
    } finally {
      this.loading = false;
    }
  }

  // ── 左侧树 ────────────────────────────────────────────────────────────────

  private providers(): Array<[string, ProviderEntry]> {
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    return Object.entries(providers).sort(([a], [b]) => a.localeCompare(b));
  }

  private renderTree(): void {
    const body = el('models-tree-body');
    body.replaceChildren();
    const providers = this.providers();
    if (!providers.length) {
      const empty = document.createElement('p');
      empty.className = 'config-empty';
      empty.textContent = '还没有供应商。用下面的「+ 供应商」添加一个。';
      body.append(empty);
    }
    for (const [name, entry] of providers) {
      const active = this.selection.provider === name && this.selection.model === null;
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'config-side-item';
      row.dataset.modelsProvider = name;
      if (active) row.setAttribute('aria-current', 'page');
      row.append(this.icon('M4 7h16M4 12h16M4 17h10', false));
      const label = document.createElement('span');
      label.className = 'config-side-text is-grow';
      label.textContent = name;
      row.append(label);
      body.append(row);
      const models = Array.isArray(entry.models) ? entry.models : [];
      models.forEach((model, index) => {
        const modelActive = this.selection.provider === name && this.selection.model === index;
        const item = document.createElement('button');
        item.type = 'button';
        item.className = 'config-side-item models-indent';
        item.dataset.modelsProvider = name;
        item.dataset.modelsModel = String(index);
        if (modelActive) item.setAttribute('aria-current', 'page');
        const modelLabel = document.createElement('span');
        modelLabel.className = 'config-side-text is-grow';
        modelLabel.textContent = text(record(model).id) || '（新模型）';
        item.append(modelLabel);
        // 推理模型带一个 T 标记，与 Pi Web 同形。
        if (record(model).reasoning) {
          const tag = document.createElement('span');
          tag.className = 'models-tag';
          tag.textContent = 'T';
          item.append(tag);
        }
        body.append(item);
      });
      const add = document.createElement('button');
      add.type = 'button';
      add.className = 'config-side-item models-indent models-add';
      add.dataset.modelsAddModel = name;
      const addLabel = document.createElement('span');
      addLabel.className = 'config-side-text';
      addLabel.textContent = '+ 模型';
      add.append(addLabel);
      body.append(add);
    }
    const addProvider = document.createElement('button');
    addProvider.type = 'button';
    addProvider.className = 'config-side-item models-add';
    addProvider.dataset.modelsAddProvider = '1';
    addProvider.append(this.icon('M12 5v14M5 12h14', false));
    const addProviderLabel = document.createElement('span');
    addProviderLabel.className = 'config-side-text';
    addProviderLabel.textContent = '+ 供应商';
    addProvider.append(addProviderLabel);
    body.append(addProvider);
  }

  private icon(path: string, filled: boolean): SVGElement {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('width', '14');
    svg.setAttribute('height', '14');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '2');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    const element = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    element.setAttribute('d', path);
    svg.append(element);
    if (filled) svg.setAttribute('fill', 'currentColor');
    return svg;
  }

  // ── 右侧表单 ──────────────────────────────────────────────────────────────

  /** 切到某个面板；树里的点击与「JSON 源码」都走这里。 */
  select(panel: string): void {
    for (const item of document.querySelectorAll<HTMLElement>('[data-models-panel]')) {
      item.hidden = item.dataset.modelsPanel !== panel;
    }
    for (const item of document.querySelectorAll<HTMLElement>('#models-tree [data-models-section]')) {
      if (item.dataset.modelsPanel === panel) item.setAttribute('aria-current', 'page');
      else item.removeAttribute('aria-current');
    }
    if (panel !== 'json') this.syncTreeCurrent();
  }

  private syncTreeCurrent(): void {
    for (const item of document.querySelectorAll<HTMLElement>('#models-tree-body [data-models-provider]')) {
      const isModel = item.dataset.modelsModel !== undefined;
      const active = isModel
        ? this.selection.provider === item.dataset.modelsProvider && this.selection.model === Number(item.dataset.modelsModel)
        : this.selection.provider === item.dataset.modelsProvider && this.selection.model === null;
      if (active) item.setAttribute('aria-current', 'page'); else item.removeAttribute('aria-current');
    }
  }

  /** 选中一个 provider：把表单填成它的当前值。 */
  selectProvider(name: string): void {
    const entry = record(this.doc.providers)[name] as ProviderEntry | undefined;
    if (!entry) return;
    this.selection = { provider: name, model: null };
    el<HTMLInputElement>('mp-name').value = name;
    el<HTMLInputElement>('mp-base').value = text(entry.baseUrl) ?? '';
    el<HTMLInputElement>('mp-key').value = text(entry.apiKey) ?? '';
    el<HTMLSelectElement>('mp-api').value = text(entry.api) ?? '';
    el<HTMLTextAreaElement>('mp-headers').value = formatHeaders(entry.headers);
    this.select('provider');
  }

  /** 选中一个模型。 */
  selectModel(name: string, index: number): void {
    const entry = record(this.doc.providers)[name] as ProviderEntry | undefined;
    const model = Array.isArray(entry?.models) ? entry!.models![index] : undefined;
    if (!model) return;
    this.selection = { provider: name, model: index };
    const value = record(model);
    el<HTMLInputElement>('mm-id').value = text(value.id) ?? '';
    el<HTMLInputElement>('mm-name').value = text(value.name) ?? '';
    el<HTMLInputElement>('mm-reasoning').checked = value.reasoning === true;
    el<HTMLInputElement>('mm-image').checked = Array.isArray(value.input) && value.input.includes('image');
    el<HTMLInputElement>('mm-ctx').value = value.contextWindow ? String(value.contextWindow) : '';
    el<HTMLInputElement>('mm-max').value = value.maxTokens ? String(value.maxTokens) : '';
    this.renderThinking(value.thinkingLevelMap);
    this.select('model');
  }

  // 思考等级映射一行一个等级，三态：
  //   omit  —— 映射表里没有这个键，用 Pi 的默认行为
  //   null  —— 键存在但值为 null，该等级被显式禁用
  //   string—— 自定义值
  // 三态缺一不可：只用文本框时「没有这个键」和「值为空串」看起来一样，
  // 而 null 根本表达不出来（Pi Web 的 ThinkingLevelMapEditor 同样分三态）。
  private renderThinking(map: unknown): void {
    const box = el('mm-thinking');
    box.replaceChildren();
    const current = record(map) as Record<string, string | null>;
    for (const level of THINKING_LEVELS) {
      const present = Object.prototype.hasOwnProperty.call(current, level);
      const raw = current[level];
      const state = !present ? 'omit' : raw === null ? 'null' : 'string';
      const row = document.createElement('div');
      row.className = 'tl-row';
      row.dataset.thinkingLevel = level;
      row.dataset.state = state;
      row.style.setProperty('--tl-color', `var(--tl-${level})`);
      const id = document.createElement('span');
      id.className = 'tl-id';
      const dot = document.createElement('span');
      dot.className = 'tl-dot';
      const name = document.createElement('span');
      name.className = 'tl-name';
      name.textContent = level;
      id.append(dot, name);
      row.append(id);
      const seg = document.createElement('div');
      seg.className = 'tl-seg';
      for (const [value, label] of [['omit', '默认'], ['null', '禁用']] as const) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'tl-btn';
        button.dataset.tl = value;
        button.textContent = label;
        seg.append(button);
      }
      row.append(seg);
      const custom = document.createElement('div');
      custom.className = 'tl-custom';
      const customButton = document.createElement('button');
      customButton.type = 'button';
      customButton.className = 'tl-btn';
      customButton.dataset.tl = 'string';
      customButton.textContent = '自定义';
      const input = document.createElement('input');
      input.type = 'text';
      input.maxLength = 10;
      input.placeholder = level;
      input.spellcheck = false;
      input.value = typeof raw === 'string' ? raw : '';
      // 聚焦输入框即视为选择「自定义」：用户敲字就是想填值，
      // 不该要求他先点一下自定义按钮（Pi Web 的 onFocus 也是这个语义）。
      input.addEventListener('focus', () => { row.dataset.state = 'string'; });
      input.addEventListener('input', () => { row.dataset.state = 'string'; });
      custom.append(customButton, input);
      row.append(custom);
      box.append(row);
    }
    box.addEventListener('click', (event) => {
      const button = (event.target as Element).closest<HTMLElement>('[data-tl]');
      const row2 = button?.closest<HTMLElement>('[data-thinking-level]');
      if (!button || !row2) return;
      const next = button.dataset.tl!;
      row2.dataset.state = next;
      const input = row2.querySelector('input');
      if (!input) return;
      if (next === 'string') {
        // 切到自定义而输入框还是空的，填上等级名本身——这是最常见的取值。
        if (!input.value) input.value = row2.dataset.thinkingLevel!;
      } else {
        // 切到默认/禁用时清空：留着旧值会让人以为它仍然生效，
        // 而保存时这一行根本不会被读（Pi Web 同样把 strVal 清掉）。
        input.value = '';
      }
    });
  }

  /** 把表单当前值写回文档。只在字段真的变过时返回 true。 */
  private commitProvider(): boolean {
    const name = this.selection.provider;
    if (!name) return false;
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    const entry = providers[name];
    if (!entry) return false;
    const nextName = el<HTMLInputElement>('mp-name').value.trim();
    if (!nextName) throw new Error('供应商名称不能为空');
    const updated: ProviderEntry = { ...entry };
    const base = el<HTMLInputElement>('mp-base').value.trim();
    const key = el<HTMLInputElement>('mp-key').value;
    const api = el<HTMLSelectElement>('mp-api').value;
    const headers = parseHeaders(el<HTMLTextAreaElement>('mp-headers').value);
    if (base) updated.baseUrl = base; else delete updated.baseUrl;
    if (key) updated.apiKey = key; else delete updated.apiKey;
    if (api) updated.api = api; else delete updated.api;
    if (Object.keys(headers).length) updated.headers = headers; else delete updated.headers;
    // 改名要连带移动键：整份文档按名字索引，只改表单不改键会让保存写到别处。
    const next: Record<string, ProviderEntry> = {};
    for (const [key2, value] of Object.entries(providers)) next[key2 === name ? nextName : key2] = value;
    next[nextName] = updated;
    this.doc = { ...this.doc, providers: next };
    if (nextName !== name) this.selection = { provider: nextName, model: null };
    return true;
  }

  private commitModel(): boolean {
    const name = this.selection.provider;
    const index = this.selection.model;
    if (!name || index === null) return false;
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    const entry = providers[name];
    if (!entry || !Array.isArray(entry.models)) return false;
    const id = el<HTMLInputElement>('mm-id').value.trim();
    if (!id) throw new Error('模型 ID 不能为空');
    const models = entry.models.map((model, i) => {
      if (i !== index) return model;
      const next: ModelEntry = { ...record(model) as ModelEntry };
      next.id = id;
      const display = el<HTMLInputElement>('mm-name').value.trim();
      if (display) next.name = display; else delete next.name;
      if (el<HTMLInputElement>('mm-reasoning').checked) next.reasoning = true; else delete next.reasoning;
      if (el<HTMLInputElement>('mm-image').checked) next.input = ['text', 'image']; else delete next.input;
      const ctx = Number(el<HTMLInputElement>('mm-ctx').value);
      if (Number.isFinite(ctx) && ctx > 0) next.contextWindow = ctx; else delete next.contextWindow;
      const max = Number(el<HTMLInputElement>('mm-max').value);
      if (Number.isFinite(max) && max > 0) next.maxTokens = max; else delete next.maxTokens;
      const levels: Record<string, string | null> = {};
      for (const row of el('mm-thinking').querySelectorAll<HTMLElement>('[data-thinking-level]')) {
        const level = row.dataset.thinkingLevel!;
        const state = row.dataset.state ?? 'omit';
        if (state === 'null') levels[level] = null;
        else if (state === 'string') {
          const value = (row.querySelector('input')?.value ?? '').trim();
          // 空串没有意义：要么当成没填（省略），要么用户本该选「禁用」。
          if (value) levels[level] = value;
        }
      }
      // 整表为空时删掉整个字段，与 Pi Web 的 onChange(... : undefined) 一致。
      if (Object.keys(levels).length) next.thinkingLevelMap = levels; else delete next.thinkingLevelMap;
      return next;
    });
    this.doc = { ...this.doc, providers: { ...providers, [name]: { ...entry, models } } };
    return true;
  }

  /** 树上的点击分发。由 workbench 的点击监听调用。 */
  handleClick(target: HTMLElement): boolean {
    const providerRow = target.closest<HTMLElement>('[data-models-provider]');
    if (providerRow) {
      const name = providerRow.dataset.modelsProvider!;
      const modelIndex = providerRow.dataset.modelsModel;
      if (modelIndex === undefined) this.selectProvider(name); else this.selectModel(name, Number(modelIndex));
      return true;
    }
    const addModel = target.closest<HTMLElement>('[data-models-add-model]');
    if (addModel) { this.addModel(addModel.dataset.modelsAddModel!); return true; }
    const addProvider = target.closest<HTMLElement>('[data-models-add-provider]');
    if (addProvider) { this.addProvider(); return true; }
    return false;
  }

  private addModel(provider: string): void {
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    const entry = providers[provider];
    if (!entry) return;
    const models = Array.isArray(entry.models) ? [...entry.models] : [];
    models.push({ id: '', name: '' });
    this.doc = { ...this.doc, providers: { ...providers, [provider]: { ...entry, models } } };
    this.renderTree();
    this.selectModel(provider, models.length - 1);
  }

  private addProvider(): void {
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    let name = 'new-provider';
    let suffix = 2;
    while (providers[name]) name = `new-provider-${suffix++}`;
    this.doc = { ...this.doc, providers: { ...providers, [name]: { api: 'openai-completions', models: [] } } };
    this.renderTree();
    this.selectProvider(name);
  }

  /** 删除当前选中的供应商。树与文档一起更新，删完回到空态。 */
  deleteProvider(): void {
    const name = this.selection.provider;
    if (!name || this.selection.model !== null) return;
    const providers = { ...record(this.doc.providers) } as Record<string, ProviderEntry>;
    delete providers[name];
    this.doc = { ...this.doc, providers };
    this.selection = { provider: null, model: null };
    this.renderTree();
    this.select('empty');
  }

  /** 删除当前选中的模型。删完回到该供应商的表单。 */
  deleteModel(): void {
    const name = this.selection.provider;
    const index = this.selection.model;
    if (!name || index === null) return;
    const providers = record(this.doc.providers) as Record<string, ProviderEntry>;
    const entry = providers[name];
    if (!entry || !Array.isArray(entry.models)) return;
    const models = entry.models.filter((_, i) => i !== index);
    this.doc = { ...this.doc, providers: { ...providers, [name]: { ...entry, models } } };
    this.selection = { provider: name, model: null };
    this.renderTree();
    this.selectProvider(name);
  }

  async save(): Promise<void> {
    // 两条保存路径：JSON 面板直接保存编辑器内容（逃生门，不要求先选中），
    // 表单面板先把字段写回文档再保存。少任何一条都会让另一条走不通。
    const onJson = !(document.querySelector('[data-models-panel=json]') as HTMLElement | null)?.hidden;
    // JSON 路径解析编辑器内容；表单路径把字段写回 this.doc。
    // 注意 commit* 是整体替换 this.doc（生成新对象），所以写请求必须在这之后
    // 才取 this.doc——先取引用会拿到 commit 之前的旧文档，改过的字段全部丢失。
    let parsed: unknown;
    if (onJson) {
      try {
        parsed = JSON.parse(el<HTMLTextAreaElement>('models-editor').value);
      } catch (error) {
        el('models-status').textContent = `不是合法 JSON：${error instanceof Error ? error.message : '解析失败'}`;
        return;
      }
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        el('models-status').textContent = '配置必须是 JSON 对象';
        return;
      }
    } else if (this.selection.provider) {
      try {
        if (this.selection.model === null) this.commitProvider(); else this.commitModel();
      } catch (error) {
        el('models-status').textContent = error instanceof Error ? error.message : '表单内容无效';
        return;
      }
    } else {
      el('models-status').textContent = '请先选择要保存的供应商或模型。';
      return;
    }
    el('models-status').textContent = '正在保存…';
    try {
      await this.bridge.request('config.models.write', '', { config: onJson ? parsed : this.doc }, 30_000);
      el('models-status').textContent = '';
      this.renderTree();
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
      const entry = record(this.doc.providers)[this.selection.provider ?? ''] as ProviderEntry | undefined;
      const baseUrl = el<HTMLInputElement>('mp-base').value.trim() || text(entry?.baseUrl) || '';
      const api = el<HTMLSelectElement>('mp-api').value || text(entry?.api) || '';
      const apiKey = el<HTMLInputElement>('mp-key').value || text(entry?.apiKey) || '';
      const headers = parseHeaders(el<HTMLTextAreaElement>('mp-headers').value || formatHeaders(entry?.headers));
      if (!baseUrl) throw new Error('请先填写 Base URL');
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
      const entry = record(this.doc.providers)[this.selection.provider ?? ''] as ProviderEntry | undefined;
      const baseUrl = el<HTMLInputElement>('mp-base').value.trim() || text(entry?.baseUrl) || '';
      const api = el<HTMLSelectElement>('mp-api').value || text(entry?.api) || '';
      const apiKey = el<HTMLInputElement>('mp-key').value || text(entry?.apiKey) || '';
      const headers = parseHeaders(el<HTMLTextAreaElement>('mp-headers').value || formatHeaders(entry?.headers));
      if (!baseUrl) throw new Error('请先填写 Base URL');
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
