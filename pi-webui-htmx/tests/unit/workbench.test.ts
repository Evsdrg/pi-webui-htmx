import { beforeAll, beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { Workbench } from '@/modules/workbench';
import { readDraft, saveDraft } from '@/modules/layout';

const fake = vi.hoisted(() => ({ request: vi.fn(), instance: undefined as EventTarget | undefined }));
vi.mock('@/modules/bridge', () => ({
 BridgeClient: class extends EventTarget {
  connected = true;
  constructor() { super(); fake.instance = this; }
  request = fake.request;
  connect() { this.dispatchEvent(new Event('connected')); }
  dispose() {}
 },
 BridgeError: class extends Error { constructor(readonly code: string, message: string) { super(message); } },
}));

let workbench: Workbench;
let pending: string[];
let busy: boolean;
let sequence: number;
const methods = ['session.start','session.prompt','session.abort','session.fork','session.navigate','sessions.delete','session.subscribe','session.set_model','sessions.search','worker.list','session.set_name','session.state','session.thinking_levels','session.pending_dialogs','session.ext_status','session.ui_response','session.stats','session.set_queue_mode','session.set_thinking','session.stop','session.set_auto_compaction','session.set_auto_retry','session.abort_retry','session.export_html','session.compact','session.bash','config.models.raw','config.models.write','config.models.discover','config.models.test'];
function emit(type: string, extra: Record<string, unknown> = {}) {
 fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version:1,kind:'event',event:'pi.event',sessionId:'s1',epoch:'test',seq:++sequence,data:{type,...extra} } }));
}
// emitRaw 指定 epoch 与 seq：用于区分「当前 worker」与「上一个 worker 的延迟帧」。
function emitRaw(epoch: string, seq: number, data: Record<string, unknown>) {
 fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version:1,kind:'event',event:'pi.event',sessionId:'s1',epoch,seq,data } }));
}
// emitNamed 指定事件名：用于 bridge.event_omitted 这类非 pi.event 的流内事件。
function emitNamed(event: string, data: Record<string, unknown>) {
 fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version:1,kind:'event',event,sessionId:'s1',epoch:'test',seq:++sequence,data } }));
}
function mount() {
 document.body.innerHTML = `<form id=auth-form><input id=bridge-token><button>连接</button></form><dialog id=auth-dialog></dialog><div id=auth-error></div>
 <form id=composer><textarea id=prompt></textarea><div id=attachments hidden></div><p id=composer-drop hidden></p><input id=attach-input type=file><button id=send-button></button><button id=abort-button></button><select id=model-select><option value="">Pi 默认模型</option></select><select id=thinking-select></select><select id=tool-preset-quick><option value=chat-only>仅聊天</option><option value=read-only>只读</option><option value=default selected>默认</option><option value=full>完整</option></select></form>
 <div id=history-scope hidden><button type=button data-action=branch-current>返回最新</button></div><div id=edit-scope hidden><span>正在编辑较早的消息</span></div><div id=unsaved-branch hidden>会话尚未写盘；首条回复前关闭工作进程会丢失这个临时分支。</div>
 <form id=new-form><input id=cwd-input></form><dialog id=new-dialog></dialog><datalist id=workspace-roots></datalist><input id=session-search>
 <button class=icon-btn data-action=session-menu aria-label=会话操作>···</button><dialog id=session-dialog><div class=session-action-grid><button data-action=compact>压缩</button><button data-action=clone>克隆</button><button data-action=export>导出</button><button data-action=stop>释放</button><button data-action=abort-retry>中止重试</button></div></dialog>
 <dialog id=rename-dialog><input id=rename-input><button data-action=rename-cancel>取消</button><button data-action=rename-save>保存</button></dialog>
 <section data-settings-panel=general><label class=switch><input type=checkbox id=default-auto-compaction checked><span>自动压缩</span></label><label class=switch><input type=checkbox id=default-auto-retry><span>自动重试</span></label><fieldset class=queue-modes><label class=switch><input type=radio name=default-queue-kind value=steering checked><span>插入指令</span></label><label class=switch><input type=radio name=default-queue-kind value=followUp><span>完成后追加</span></label></fieldset></section>
 <div class=queue-hint id=queue-hint hidden></div>
 <header class=topbar><nav class=topbar-tools aria-label=功能区><button data-action=full-history>完整历史</button><button data-action=panel-title aria-controls=panel-title aria-expanded=false>生成标题</button><button data-action=panel-system aria-controls=panel-system aria-expanded=false>系统</button><button data-action=panel-tools aria-controls=panel-tools aria-expanded=false>工具</button></nav><button id=context-usage data-action=panel-info aria-controls=panel-info aria-expanded=false hidden></button><span id=session-state class=state>就绪</span></header>
 <section id=panel-info hidden><button data-action=panel-info-close></button><dl id=session-facts></dl></section>
 <section id=panel-title hidden><button data-action=panel-title-close></button><form id=title-form><input id=local-title></form></section><section id=panel-system hidden><button data-action=panel-system-close></button><div id=system-body></div></section><section id=panel-tools hidden><button data-action=panel-tools-close></button><div id=tools-body></div></section>
 <dialog id=models-dialog><p id=models-status></p><textarea id=models-editor></textarea>
<input id=mp-name><input id=mp-base><input id=mp-key type=password><select id=mp-api><option value=""></option><option value="openai-completions">openai-completions</option><option value="openai-responses">openai-responses</option></select><textarea id=mp-headers></textarea>
<input id=mm-id><input id=mm-name><input type=checkbox id=mm-reasoning><input type=checkbox id=mm-image><input id=mm-ctx><input id=mm-max><div id=mm-thinking></div>
<div id=discover-result></div><aside class=config-side><div class=config-side-list id=models-tree><div id=models-tree-body></div></div></aside>
<section data-models-panel=provider hidden></section><section data-models-panel=model hidden></section><section data-models-panel=json hidden></section><section data-models-panel=empty hidden></section>
<button data-action=models-edit>编辑</button><button data-action=models-reload>重读</button><button data-action=models-save>保存</button><button data-action=models-discover>发现</button><button data-action=models-test>测试</button></dialog>`;
 for(const id of ['live','conn-state','connection-notice','session-state','session-title','session-cwd','session-list','session-count','turns','older-slot','chat-scroll','welcome','command-menu','ext-status-slot','ext-widgets-before','ext-widgets-after','ext-dialog-slot','usage','toast-root']) {const node=document.createElement('div');node.id=id;document.body.append(node);}
{for(const id of ['search-query','system-session','tools-session','stats-session','branch-session','files-path','git-path','diff-path']){const node=document.createElement('input');node.type='hidden';node.id=id;document.body.append(node);}}
 document.body.insertAdjacentHTML('beforeend', readFileSync('src/templates/shell.html', 'utf8').match(/<template id="thinking-row-template">[\s\S]*?<\/template>/)![0]);
 document.body.dataset.sessionId='s1';
 Object.defineProperty(HTMLDialogElement.prototype,'showModal',{configurable:true,value:function(this:HTMLDialogElement){this.open=true;this.dataset.modal='true';}});
 Object.defineProperty(HTMLDialogElement.prototype,'close',{configurable:true,value:function(this:HTMLDialogElement){this.open=false;delete this.dataset.modal;}});
 const matches=Element.prototype.matches;
 vi.spyOn(Element.prototype,'matches').mockImplementation(function(this:Element,selector){return selector===':modal' ? this.getAttribute('data-modal')==='true' : matches.call(this,selector);});
 window.htmx = {trigger:vi.fn(),ajax:vi.fn(async (_method:string,url:string) => {
  if(url.startsWith('/ui/extensions/dialogs')) {
   const root=document.getElementById('ext-dialog-slot')!;
   root.innerHTML='<dialog data-dialog-id=dialog-1 data-method=input><form data-extension-form><input name=value><button type=submit name=confirmed value=true>确定</button></form></dialog>';
   document.dispatchEvent(new CustomEvent('htmx:afterSwap',{detail:{target:root}}));
  }
 })} as unknown as typeof window.htmx;
}

// 顶栏面板是动态 import 的独立分块；先预热模块缓存，
// 否则假定时器下测试要等一个真实模块加载才能看到 DOM 变化。
//
// 放在 beforeAll 而不是 beforeEach：模块缓存跨用例共享，每个用例都 import
// 一次等于把同一份解析工作重复 82 遍（实测这一项占了本文件 6.6 秒中的绝大部分）。
beforeAll(async () => { await import('../../src/modules/topbar'); });

// 本文件的等待几乎都是「同步 DOM/调用状态很快就会出现」，而 vi.waitFor 默认
// 50ms 轮询一次——80 多处累积起来是本文件的主要耗时（实测占 6.9 秒的八成）。
// 用 1ms 间隔：语义不变（仍然是重试到不抛错为止），只是不再白等。
const waitFor = (cond: () => unknown) => vi.waitFor(cond, { interval: 1 });

beforeEach(async () => {
 // 全局默认（自动压缩/重试/排队方式）现在是 localStorage 偏好：
 // 不清掉会把上一用例的选择带进下一用例，导致「默认值」断言假失败。
 localStorage.clear();
 vi.useFakeTimers(); pending=[];busy=false;sequence=0;mount();
 vi.stubGlobal('fetch',vi.fn(async () => new Response(JSON.stringify({version:1,methods}),{status:200})));
 fake.request.mockImplementation(async (method:string) => {
  if(method==='worker.list')return[{sessionId:'s1',cwd:'/fixture',busy}];
  if(method==='session.state')return{sessionId:'s1',sessionName:'隔离会话',isStreaming:busy,isCompacting:false,steeringMode:'all',followUpMode:'all',autoCompactionEnabled:true};
  if(method==='session.thinking_levels')return['off','high'];
  if(method==='config.models.raw')return{providers:{cpa:{api:'openai-completions',baseUrl:'https://example.com/v1',apiKey:'***',models:[{id:'m1',name:'旧名字',reasoning:true,thinkingLevelMap:{off:'off',low:null,high:'high'}},{id:'m2',name:'第二个'}]}}};
  if(method==='config.models.write')return{written:true};
  if(method==='config.models.discover')return{models:[{id:'gpt-x',name:'GPT X'}]};
  if(method==='config.models.test')return{ok:true,message:'连通正常'};
  if(method==='session.pending_dialogs')return{ids:[...pending]};
  if(method==='session.start')return{sessionId:'s1',cwd:'/fixture',epoch:'e1',pid:1,status:'idle',busy:false,seq:0};
  if(method==='session.ui_response'){pending=[];busy=false;emit('agent_settled');return{answered:true};}
  return{};
 });
 workbench=new Workbench(vi.fn());workbench.start();
 // 目录选择器与「新建会话」在按需分块里接线：预载模块并冲刷微任务，
 // 让 start() 里的动态 import().then 完成注册，后续用例才能派发 dir-list 事件。
 await import('../../src/modules/new-session');
 for(let i=0;i<5;i++) await Promise.resolve();
 await waitFor(()=>expect(fake.request).toHaveBeenCalledWith('session.state','s1',undefined,30_000));
});
afterEach(()=>{workbench?.dispose();vi.useRealTimers();vi.unstubAllGlobals();document.body.replaceChildren();});

describe('扩展对话交互回归',()=>{
 it('状态轮询不替换仍在编辑的同一对话',async()=>{
  pending=['dialog-1'];busy=true;emit('extension_ui_request',{id:'dialog-1',method:'input',title:'请输入'});
  await waitFor(()=>expect(document.querySelector('#ext-dialog-slot input')).not.toBeNull());
  const input=document.querySelector<HTMLInputElement>('#ext-dialog-slot input')!;input.value='尚未提交的内容';
  await vi.advanceTimersByTimeAsync(15_000);
  expect(document.querySelector('#ext-dialog-slot input')).toBe(input);
  expect(input.value).toBe('尚未提交的内容');
  const requests=vi.mocked(window.htmx.ajax).mock.calls.filter((call)=>String(call[1]).startsWith('/ui/extensions/dialogs'));
  expect(requests).toHaveLength(1);
 });
 it('回执期间已结束的任务不会被重新标记为运行中',async()=>{
  pending=['dialog-1'];busy=true;emit('extension_ui_request',{id:'dialog-1',method:'confirm'});
  await waitFor(()=>expect(document.querySelector('#ext-dialog-slot button')).not.toBeNull());
  document.querySelector<HTMLButtonElement>('#ext-dialog-slot button')!.click();
  await waitFor(()=>expect(fake.request).toHaveBeenCalledWith('session.ui_response','s1',{id:'dialog-1',confirmed:true},30_000));
  await vi.advanceTimersByTimeAsync(100);
  expect(document.getElementById('session-state')?.textContent).toBe('就绪');
  expect(document.querySelector('#ext-dialog-slot dialog')).toBeNull();
 });
});

describe('目录浏览的提交边界', () => {
  it('错误片段不回填旧路径，也不能提交未成功浏览的目录', () => {
    document.body.insertAdjacentHTML('beforeend', '<input id="dir-current" value="/ok"><div id="dir-list"><p>无法访问目录</p></div><button id="dir-use"></button>');
    const input = document.getElementById('cwd-input') as HTMLInputElement; input.value='/missing';
    document.getElementById('dir-list')!.dispatchEvent(new CustomEvent('htmx:afterSwap', {bubbles:true}));
    expect(input.value).toBe('/missing');expect((document.getElementById('dir-use') as HTMLButtonElement).disabled).toBe(true);
    document.getElementById('dir-list')!.innerHTML='<span hidden data-dir-loaded-path="/ok"></span>';
    document.getElementById('dir-list')!.dispatchEvent(new CustomEvent('htmx:afterSwap', {bubbles:true}));
    expect(input.value).toBe('/ok');expect((document.getElementById('dir-use') as HTMLButtonElement).disabled).toBe(false);
  });
});

describe('排队与压缩设置（全局默认）', () => {
  it('运行中发送前先把默认模式同步给桥，再带 streamingBehavior 提交', async () => {
    // send() 读的是 this.run，所以要先让界面认定在运行。
    busy = true; emit('agent_start');
    expect(document.getElementById('session-state')?.textContent).toBe('运行中');
    // 全局默认改为「完成后追加」（设置面板里的 radio）。
    document.querySelector<HTMLInputElement>('input[name="default-queue-kind"][value="followUp"]')!.click();
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '排队消息';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(vi.mocked(fake.request).mock.calls.some((c) => c[0] === 'session.prompt')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '排队消息', streamingBehavior: 'followUp' }, 30_000);
    expect(fake.request).toHaveBeenCalledWith('session.set_queue_mode', 's1', { kind: 'followUp', mode: 'one-at-a-time' }, 30_000);
    const order = vi.mocked(fake.request).mock.calls.map((c) => c[0]);
    expect(order.lastIndexOf('session.set_queue_mode')).toBeLessThan(order.lastIndexOf('session.prompt'));
  });

  it('自动压缩默认设置对当前会话即时下发并回读', async () => {
    const box = document.getElementById('default-auto-compaction') as HTMLInputElement;
    expect(box.checked).toBe(true);
    box.checked = false; box.dispatchEvent(new Event('change'));
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_auto_compaction', 's1', { enabled: false }, 30_000));
    // 成功后回读：command() 的预取状态刷新发生在 set 之前，不回读会停在旧值。
    const states = vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'session.state');
    expect(states.length).toBeGreaterThan(1);
  });

  it('自动重试默认设置对当前会话即时下发', async () => {
    const box = document.getElementById('default-auto-retry') as HTMLInputElement;
    box.checked = true; box.dispatchEvent(new Event('change'));
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_auto_retry', 's1', { enabled: true }, 30_000));
  });

  it('排队提示只在运行中出现，并随默认模式变化', async () => {
    const hint = document.getElementById('queue-hint')!;
    expect(hint.hidden).toBe(true);
    busy = true; emit('agent_start');
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    expect(hint.hidden).toBe(false);
    expect(hint.textContent).toContain('插入指令');
    const steerText = hint.textContent;
    document.querySelector<HTMLInputElement>('input[name="default-queue-kind"][value="followUp"]')!.click();
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    // 只断言文案随模式变化，不锁死具体措辞——那是实现细节。
    expect(hint.textContent).not.toBe(steerText);
    expect(hint.textContent).toContain('排到队列末尾');
  });
});

describe('模型配置编辑器', () => {
  // JSON 源码是逃生门：不要求先选中供应商或模型，直接保存编辑器内容。
  // 下面几条走的就是这条路径，所以要先切到 JSON 面板。
  const openJsonPanel = () => {
    (workbench as unknown as { models?: { select(panel: string): void } }).models?.select('json');
  };

  it('打开时读取原始配置并格式化进编辑器', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    // 密钥必须已经是打码值，页面拿不到真值。
    expect(document.getElementById('models-editor').value).toContain('"***"');
  });

  it('保存非法 JSON 时拒绝并不发请求', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    openJsonPanel();
    const before = vi.mocked(fake.request).mock.calls.length;
    (document.getElementById('models-editor') as HTMLTextAreaElement).value = '{ 这不是 JSON';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    expect(vi.mocked(fake.request).mock.calls.length).toBe(before);
    expect(document.getElementById('models-status').textContent).toContain('不是合法 JSON');
  });

  it('保存成功不以磁盘重读覆盖正在编辑的草稿', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    openJsonPanel();
    const readsBefore = vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'config.models.raw').length;
    (document.getElementById('models-editor') as HTMLTextAreaElement).value = '{"providers":{}}';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    expect(vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'config.models.raw').length).toBe(readsBefore);
    expect(document.getElementById('models-status').textContent).toContain('快照已保存');
  });

  it('自定义头部按行解析，非法格式被拒绝', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    (document.getElementById('mp-base') as HTMLInputElement).value = 'https://api.example.com/v1';
    (document.getElementById('mp-headers') as HTMLTextAreaElement).value = 'X-A: 1\n坏行\nX-B: 2';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-discover', document.createElement('button'));
    await waitFor(() => expect(document.getElementById('discover-result').textContent).toContain('名称: 值'));
    expect(vi.mocked(fake.request).mock.calls.some((c) => c[0] === 'config.models.discover')).toBe(false);
  });

  it('发现结果通过POST片段由桥渲染，凭据不进入URL', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    (document.getElementById('mp-base') as HTMLInputElement).value = 'https://api.example.com/v1';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-discover', document.createElement('button'));
    expect(window.htmx.ajax).toHaveBeenCalledWith('post', '/ui/models/discover', expect.objectContaining({ values: expect.objectContaining({baseUrl:'https://api.example.com/v1'}) }));
    expect(document.getElementById('discover-result').querySelector('script')).toBeNull();
  });
});

describe('工作区面板的展开状态', () => {
  it('点面板内部的关闭按钮也会更新页眉控制按钮', async () => {
    const shell = document.createElement('div'); shell.id = 'workbench'; document.body.append(shell);
    const toggle = document.createElement('button'); toggle.dataset.action = 'workspace';
    toggle.setAttribute('aria-controls', 'workspace-panel'); toggle.setAttribute('aria-expanded', 'true');
    document.body.append(toggle);
    const panel = document.createElement('aside'); panel.id = 'workspace-panel'; document.body.append(panel);
    const close = document.createElement('button'); close.dataset.action = 'workspace'; panel.append(close);
    await workbench.action('workspace', close);
    expect(panel.hidden).toBe(true);
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
  });
});

describe('中止提示跟随真正的运行状态', () => {
  it('agent_settled 先于 abort 回执时不重新显示等待清理', async () => {
    busy = true; emit('agent_start');
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.abort') { busy = false; emit('agent_settled'); return {}; }
      if (method === 'session.stats') return {};
      return {};
    });
    await workbench.action('abort', document.createElement('button'));
    expect(document.getElementById('session-state')?.textContent).toBe('就绪');
    expect(document.getElementById('connection-notice')?.textContent).not.toContain('等待 Pi 完成清理');
  });

  it('abort 回执先到时显示等待，agent_settled 后撤掉提示', async () => {
    busy = true; emit('agent_start');
    await workbench.action('abort', document.createElement('button'));
    expect(document.getElementById('connection-notice')?.textContent).toContain('等待 Pi 完成清理');
    busy = false; emit('agent_settled');
    expect(document.getElementById('session-state')?.textContent).toBe('就绪');
    expect(document.getElementById('connection-notice')?.textContent).not.toContain('等待 Pi 完成清理');
  });
});

describe('订阅关闭提示只保留到状态核对结束', () => {
  function closeSubscription(): void {
    fake.instance!.dispatchEvent(new CustomEvent('message', { detail: {
      version: 1, kind: 'control', event: 'bridge.subscription_closed', sessionId: 's1', data: { resyncRequired: true },
    } }));
  }

  it('先收到关闭控制帧，随后 settled 撤掉临时提示', async () => {
    busy = true; emit('agent_start'); closeSubscription();
    expect(document.getElementById('connection-notice')?.textContent).toContain('正在核对任务状态');
    busy = false; emit('agent_settled');
    expect(document.getElementById('session-state')?.textContent).toBe('就绪');
    expect(document.getElementById('connection-notice')?.textContent).toBe('');
  });

  it('关闭控制帧后仅靠状态回读变空闲也撤掉提示', async () => {
    busy = true; emit('agent_start');
    // 状态在桥侧已结束，但前端尚未收到 agent_settled。
    busy = false; closeSubscription();
    await waitFor(() => expect(document.getElementById('session-state')?.textContent).toBe('就绪'));
    expect(document.getElementById('connection-notice')?.textContent).toBe('');
  });

  it('不会抹掉需要用户核对的补发窗口失效提示', () => {
    busy = true; emit('agent_start');
    document.getElementById('connection-notice')!.textContent = '实时事件已超出补发窗口，已重新读取历史';
    busy = false; emit('agent_settled');
    expect(document.getElementById('connection-notice')?.textContent).toContain('补发窗口');
  });
});

describe('附件事件中的 FileList 必须同步快照', () => {
  const image = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1])], 'fixture.png', { type: 'image/png' });
  const expiringList = (file: File, alive: () => boolean): FileList => ({
    get length() { return alive() ? 1 : 0; },
    item(index: number) { return alive() && index === 0 ? file : null; },
    *[Symbol.iterator]() { if (alive()) yield file; },
  }) as FileList;

  it('选择器清空 input 后仍读取选中的文件', async () => {
    const input = document.getElementById('attach-input') as HTMLInputElement;
    let alive = true;
    Object.defineProperty(input, 'files', { configurable: true, value: expiringList(image(), () => alive) });
    Object.defineProperty(input, 'value', { configurable: true, get: () => '', set: (value: string) => { if (!value) alive = false; } });
    input.dispatchEvent(new Event('change'));
    await vi.advanceTimersByTimeAsync(50);
    await waitFor(() => expect(document.querySelectorAll('#attachments .attachment')).toHaveLength(1));
    expect(alive).toBe(false);
  });

  it('drop 事件结束后仍读取拖入的文件', async () => {
    let alive = true;
    const event = new Event('drop', { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'dataTransfer', { value: { files: expiringList(image(), () => alive) } });
    document.getElementById('composer')!.dispatchEvent(event);
    alive = false;
    await vi.advanceTimersByTimeAsync(50);
    await waitFor(() => expect(document.querySelectorAll('#attachments .attachment')).toHaveLength(1));
  });
});

describe('附件随消息发送', () => {
  it('有附件时 prompt 带 images，发送成功后清空', async () => {
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '看这张图';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '看这张图' }, 30_000));
    // 无附件时不得带 images 键。
    const call = vi.mocked(fake.request).mock.calls.find((c) => c[0] === 'session.prompt');
    expect(Object.keys((call?.[2] ?? {}) as object)).not.toContain('images');
    expect(document.getElementById('attachments')!.hidden).toBe(true);
  });

  it('附件容器初始隐藏，发送后仍保持隐藏', () => {
    const box = document.getElementById('attachments')!;
    expect(box.hidden).toBe(true);
    expect(box.childElementCount).toBe(0);
  });
});

describe('@ 菜单与 Enter 的按键归属', () => {
  it('菜单开着时 Enter 不提交，关闭后才提交', async () => {
    const prompt = document.getElementById('prompt') as HTMLTextAreaElement;
    // 菜单开着、且有候选：Enter 应插入而不是提交。
    (workbench as unknown as { mention?: unknown }).mention = {
      active: true,
      move: () => true,
      choose: () => true,
      hide: () => {},
      refresh: () => {},
    };
    prompt.value = '@REA';
    prompt.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    expect(fake.request).not.toHaveBeenCalledWith('session.prompt', expect.anything(), expect.anything(), expect.anything());

    // 菜单开着但没有候选：choose 返回 false，仍然不得提交。
    (workbench as unknown as { mention?: unknown }).mention = {
      active: true, move: () => false, choose: () => false, hide: () => {}, refresh: () => {},
    };
    prompt.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    const promptCalls = vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'session.prompt');
    expect(promptCalls).toHaveLength(0);

    // 菜单关闭：Enter 恢复提交。
    (workbench as unknown as { mention?: unknown }).mention = {
      active: false, move: () => false, choose: () => false, hide: () => {}, refresh: () => {},
    };
    prompt.value = '普通消息';
    prompt.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '普通消息' }, 30_000));
  });
});

it('默认排队模式用协议一致的 steering，不是 steer', async () => {
  // B03：前端曾经发送 kind=steer，桥只接受 steering/followUp，
  // 运行中的「插入指令」因此被整体拒绝。这里锁定默认取值。
  busy = true; emit('agent_start');
  expect(document.querySelector<HTMLInputElement>('input[name="default-queue-kind"]:checked')!.value).toBe('steering');
  (document.getElementById('prompt') as HTMLTextAreaElement).value = '插入一条';
  document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
  await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_queue_mode', 's1', { kind: 'steering', mode: 'all' }, 30_000));
  expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '插入一条', streamingBehavior: 'steer' }, 30_000);
  // 旧值必须不再出现。
  for (const call of vi.mocked(fake.request).mock.calls) {
    expect((call[2] as { kind?: string } | undefined)?.kind).not.toBe('steer');
  }
});

describe('搜索结果归属与定位', () => {
  it('保留标题和目录，点击助手命中后定位到所属回合', async () => {
    const search = document.getElementById('session-search') as HTMLInputElement;
    search.value = 'Project overview';
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [];
      return {};
    });
    await workbench.search('Project overview');
    // 搜索结果由桥渲染（/ui/search）并换进 #session-list；
    // 这里按片段落地的时序把服务端输出放进 DOM。
    expect(document.getElementById('search-query')!.getAttribute('value')).toBe('Project overview');
    document.getElementById('session-list')!.innerHTML =
      '<a class="session-item" href="/?session=s2&amp;leafId=a1" data-session="s2" data-entry-id="a1" data-title="Sample workspace review" data-cwd="/fixture">' +
      '<span class="session-title">Sample workspace review</span><span class="session-meta">/fixture</span><span class="search-snippet">Project overview</span></a>';
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target: document.getElementById('session-list') } }));
    const link = document.querySelector<HTMLAnchorElement>('#session-list [data-session="s2"]')!;
    expect(link.textContent).toContain('Sample workspace review');
    expect(link.dataset.title).toBe('Sample workspace review');
    expect(link.dataset.cwd).toBe('/fixture');
    link.click();
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s2'));
    // 会话名不再有独立的显示节点：侧栏列表负责显示，Workbench 只保存内部状态
    // （重命名流程与搜索结果标题都用它）。
    expect(workbench.title()).toBe('Sample workspace review');
    expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => String(call[1]).includes('/ui/sessions/s2/history?leafId=a1'))).toBe(true);

    const turns = document.getElementById('turns')!;
    turns.innerHTML = '<article data-turn-id="u1"><span hidden data-search-entry-id="a1"></span>回复</article>';
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: {
      target: turns, xhr: { responseURL: `${location.origin}/ui/sessions/s2/history?leafId=a1` },
    } }));
    expect(turns.querySelector('[data-turn-id="u1"]')?.classList.contains('search-target')).toBe(true);
    expect(document.getElementById('history-scope')?.hidden).toBe(false);
    document.querySelector<HTMLButtonElement>('#history-scope button')!.click();
    await waitFor(() => expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => call[1] === '/ui/sessions/s2/history')).toBe(true));
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: {
      target: turns, xhr: { responseURL: `${location.origin}/ui/sessions/s2/history` },
    } }));
    expect(document.getElementById('history-scope')?.hidden).toBe(true);
  });
});

describe('新会话首次发送', () => {
  it('首次选择目录不丢掉已输入的草稿和模型', async () => {
    saveDraft('new:', ''); saveDraft('new:/fixture', '');
    workbench.selectSession('', '', '新会话', false);
    const model = document.getElementById('model-select') as HTMLSelectElement;
    const option = new Option('DeepSeek Flash', 'CPA-Responses/deepseek-flash');
    option.dataset.provider = 'CPA-Responses'; option.dataset.modelId = 'deepseek-flash';
    model.add(option); model.value = option.value;
    model.dispatchEvent(new Event('change', { bubbles: true }));
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    input.value = '选择目录前写下的草稿';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector<HTMLInputElement>('input[name="default-queue-kind"][value="followUp"]')!.click();
    (document.getElementById('cwd-input') as HTMLInputElement).value = '/fixture';
    document.getElementById('new-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    // 提交走动态分块（new-session）：模块已预载，推进宏任务让 import 链完成。
    await vi.advanceTimersByTimeAsync(0);
    expect(input.value).toBe('选择目录前写下的草稿');
    expect(model.value).toBe('CPA-Responses/deepseek-flash');
    // 排队方式是全局默认：新建会话不改变它。
    expect(document.querySelector<HTMLInputElement>('input[name="default-queue-kind"]:checked')?.value).toBe('followUp');
    expect(readDraft('new:/fixture')).toBe(input.value);
    saveDraft('new:', ''); saveDraft('new:/fixture', '');
  });

  it('Pi 分配的新 ID 属于同一发送事务，先设置模型再提交消息', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') return { sessionId: 's-new', cwd: '/fixture' };
      if (method === 'session.state') return { sessionId: 's-new', model: { provider: 'CPA-Responses', id: 'deepseek-flash', name: 'DeepSeek Flash' } };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      return {};
    });
    workbench.selectSession('', '/fixture', '新会话');
    const select = document.getElementById('model-select') as HTMLSelectElement;
    const option = new Option('DeepSeek Flash', 'CPA-Responses/deepseek-flash');
    option.dataset.provider = 'CPA-Responses'; option.dataset.modelId = 'deepseek-flash';
    select.add(option); select.value = option.value;
    select.dispatchEvent(new Event('change', { bubbles: true }));
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '首条消息';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.start')).toBe(true));
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.prompt')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.set_model', 's-new', { provider: 'CPA-Responses', modelId: 'deepseek-flash' }, 30_000);
    expect(fake.request).toHaveBeenCalledWith('session.prompt', 's-new', { text: '首条消息' }, 30_000);
    expect(document.body.dataset.sessionId).toBe('s-new');
    expect(document.getElementById('connection-notice')?.textContent).not.toContain('未发送');
  });
});

describe('从用户消息创建未落盘分支', () => {
  it('保留 Pi 返回的原消息，临时分支不读取不存在的 JSONL', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      if (method === 'session.fork') return { sessionId: 's-fork', text: '重写 src/main.ts', persisted: false };
      if (method === 'worker.list') return [{ sessionId: 's-fork', cwd: '/fixture', busy: false }];
      if (method === 'session.state') return { sessionId: 's-fork', isStreaming: false, model: { provider: 'cpa', id: 'm1', name: '测试模型' } };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      return {};
    });
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    input.value = '原会话尚未提交的草稿';
    await workbench.forkFrom('u1');
    expect(fake.request).toHaveBeenCalledWith('session.fork', 's1', { entryId: 'u1' }, 30_000);
    expect(document.body.dataset.sessionId).toBe('s-fork');
    expect(input.value).toBe('重写 src/main.ts');
    expect(document.getElementById('unsaved-branch')?.hidden).toBe(false);
    expect(document.getElementById('unsaved-branch')?.textContent).toContain('尚未写盘');
    expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => String(call[1]).includes('/ui/sessions/s-fork/history'))).toBe(false);
  });

  it('刷新后的活跃未落盘分支收到 204 时不显示会话不存在', () => {
    workbench.selectSession('s-fork', '/fixture', '分支会话', false);
    const target = document.getElementById('turns')!;
    const detail = { target, xhr: {
      responseURL: `${location.origin}/ui/sessions/s-fork/history`, status: 204,
      getResponseHeader: (name: string) => name === 'X-Session-Unsaved' ? '1' : null,
    }, shouldSwap: true };
    document.dispatchEvent(new CustomEvent('htmx:beforeSwap', { detail }));
    expect(detail.shouldSwap).toBe(false);
    expect(document.getElementById('unsaved-branch')?.hidden).toBe(false);
    expect(document.getElementById('connection-notice')?.textContent).not.toContain('会话不存在');
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: {
      target, xhr: { responseURL: `${location.origin}/ui/sessions/s-fork/history`, status: 200 },
    } }));
    expect(document.getElementById('unsaved-branch')?.hidden).toBe(true);
    expect(workbench.diskSession).toBe(true);
  });

  it('已有 assistant 的分支仍读取磁盘历史，并预填原消息', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      if (method === 'session.fork') return { sessionId: 's-fork', text: '修改后的原消息', persisted: true };
      if (method === 'worker.list') return [{ sessionId: 's-fork', cwd: '/fixture', busy: false }];
      if (method === 'session.state') return { sessionId: 's-fork', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      return {};
    });
    await workbench.forkFrom('u2');
    expect(document.body.dataset.sessionId).toBe('s-fork');
    expect((document.getElementById('prompt') as HTMLTextAreaElement).value).toBe('修改后的原消息');
    expect(document.getElementById('unsaved-branch')?.hidden).toBe(true);
    expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => String(call[1]).includes('/ui/sessions/s-fork/history'))).toBe(true);
  });
});

describe('从此处编辑（会话内跳转）与删除会话', () => {
  // 用户回合的完整结构：图片入口与两个按钮都在，回填原文必须只取正文。
  const turnHTML = `<article class=turn data-turn-id=u1>
   <div class=turn-user><div class=bubble>原消息正文<div class=user-image-entry><button class=lazy-block>查看附带图片</button></div></div>
   <div class=turn-actions><button class=icon-btn data-action=edit-here data-entry-id=u1>从此处编辑</button><button class=icon-btn data-action=fork data-entry-id=u1>新建会话</button></div></div></article>`;

  const navigateMock = (leafId: string, previousLeafId: string) => {
    fake.request.mockImplementation(async (method: string) => {
      // 忙会话的拒绝在桥侧（与生产一致）：夹具同样报错而不是成功。
      if (method === 'session.navigate') {
        if (busy) throw new Error('会话正在运行，请先等待当前回合结束');
        return { leafId, previousLeafId };
      }
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture', busy: false }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      return {};
    });
  };

  it('跳转成功后回填原文（不含图片按钮文案）、进入编辑态并显示新叶子视图', async () => {
    navigateMock('a1', 'a9');
    document.getElementById('turns')!.innerHTML = turnHTML;
    document.querySelector<HTMLButtonElement>('[data-action=edit-here]')!.click();
    await waitFor(() => expect(fake.request.mock.calls.some((call) => call[0] === 'session.navigate')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.navigate', 's1', { entryId: 'u1' }, 30_000);
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    expect(input.value).toBe('原消息正文');
    expect(document.getElementById('edit-scope')?.hidden).toBe(false);
    expect(document.getElementById('history-scope')?.hidden).toBe(true);
    // 跳转后按新叶子重读历史：视图截断到编辑点。
    await waitFor(() => expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => String(call[1]).includes('/ui/sessions/s1/history?leafId=a1'))).toBe(true));
  });

  it('发送后退出编辑态：分叉已落定，横幅收起', async () => {
    navigateMock('a1', 'a9');
    document.getElementById('turns')!.innerHTML = turnHTML;
    document.querySelector<HTMLButtonElement>('[data-action=edit-here]')!.click();
    await waitFor(() => expect(document.getElementById('edit-scope')?.hidden).toBe(false));
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    input.value = '改写后的消息';
    document.getElementById('composer')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await waitFor(() => expect(fake.request.mock.calls.some((call) => call[0] === 'session.prompt')).toBe(true));
    expect(document.getElementById('edit-scope')?.hidden).toBe(true);
  });

  it('编辑第一条消息（根叶子）时清空视图且不发历史请求', async () => {
    navigateMock('', 'a1');
    document.getElementById('turns')!.innerHTML = turnHTML;
    document.querySelector<HTMLButtonElement>('[data-action=edit-here]')!.click();
    await waitFor(() => expect(fake.request.mock.calls.some((call) => call[0] === 'session.navigate')).toBe(true));
    expect(document.getElementById('turns')!.children.length).toBe(0);
    expect(vi.mocked(window.htmx.ajax).mock.calls.some((call) => String(call[1]).includes('leafId='))).toBe(false);
    expect(document.getElementById('edit-scope')?.hidden).toBe(false);
  });

  it('会话运行中由桥拒绝编辑且不覆盖输入框', async () => {
    navigateMock('a1', 'a9');
    document.getElementById('turns')!.innerHTML = turnHTML;
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    input.value = '用户正在写的内容';
    busy = true; emit('agent_start');
    document.querySelector<HTMLButtonElement>('[data-action=edit-here]')!.click();
    // 拒绝以桥的错误为准：等它出现在提示里，确认走的是「回填之前失败」的路径。
    await waitFor(() => expect(document.getElementById('toast-root')?.textContent).toContain('当前回合'));
    expect(fake.request).toHaveBeenCalledWith('session.navigate', 's1', { entryId: 'u1' }, 30_000);
    expect(input.value).toBe('用户正在写的内容');
  });

  it('侧栏行删除会话带 force，运行中也删得掉', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    // 删除/改名已移到会话行：构造一行并点它的删除按钮。
    document.getElementById('session-list')!.innerHTML =
      '<div class="session-item" data-session="s1" data-cwd="/fixture" data-title="会话一"><a class="session-link" href="?session=s1"><span class="session-title">会话一</span></a><span class="session-ops"><button data-action="session-rename"></button><button data-action="session-delete"></button></span></div>';
    document.querySelector<HTMLElement>('[data-action=session-delete]')!.click();
    await waitFor(() => expect(fake.request.mock.calls.some((call) => call[0] === 'sessions.delete')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('sessions.delete', '', { sessionId: 's1', force: true }, 30_000);
  });

  it('删除前的确认文案说明会强制停止工作进程', async () => {
    const confirmSpy = vi.fn(() => false);
    vi.stubGlobal('confirm', confirmSpy);
    document.getElementById('session-list')!.innerHTML =
      '<div class="session-item" data-session="s1" data-cwd="/fixture"><a class="session-link" href="?session=s1"><span class="session-title">会话一</span></a><span class="session-ops"><button data-action="session-delete"></button></span></div>';
    document.querySelector<HTMLElement>('[data-action=session-delete]')!.click();
    await waitFor(() => expect(confirmSpy).toHaveBeenCalled());
    expect(String(confirmSpy.mock.calls[0]?.[0])).toContain('会被强制停止');
    expect(fake.request.mock.calls.some((call) => call[0] === 'sessions.delete')).toBe(false);
  });

  it('重命名非当前会话：先拉起 worker，改完立即释放', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return []; // 目标会话没有活动 worker
      if (method === 'session.state') return { sessionId: 'other', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      if (method === 'session.start') return { sessionId: 'other', cwd: '/tmp/other' };
      return {};
    });
    document.getElementById('session-list')!.innerHTML =
      '<div class="session-item" data-session="other" data-cwd="/tmp/other" data-title="旧名"><a class="session-link" href="?session=other"><span class="session-title">旧名</span></a><span class="session-ops"><button data-action="session-rename"></button></span></div>';
    document.querySelector<HTMLElement>('[data-action=session-rename]')!.click();
    const input = document.getElementById('rename-input') as HTMLInputElement;
    expect(input.value).toBe('旧名');
    input.value = '新名字';
    document.querySelector<HTMLElement>('[data-action=rename-save]')!.click();
    await waitFor(() => expect(fake.request.mock.calls.some((call) => call[0] === 'session.set_name')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.set_name', 'other', { name: '新名字' }, 30_000);
    // 改名依赖活动进程：本次为改名拉起的那一个，改完立即释放。
    expect(fake.request.mock.calls.some((call) => call[0] === 'session.start' && call[1] === 'other')).toBe(true);
    expect(fake.request).toHaveBeenCalledWith('session.stop', 'other', { force: false }, 30_000);
  });
});

describe('历史模型与当前可用模型', () => {
  const missingModel = { provider: 'unknown', id: 'unknown', name: 'unknown' };

  it('离线历史只显示所选分支的历史模型，不启动 worker', () => {
    const turns = document.getElementById('turns')!;
    turns.innerHTML = '<span hidden data-history-model-provider="CPA-Responses" data-history-model-id="deepseek-flash"></span>';
    fake.request.mockClear();
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: {
      target: turns, xhr: { responseURL: `${location.origin}/ui/sessions/s1/history` },
    } }));
    const selected = (document.getElementById('model-select') as HTMLSelectElement).selectedOptions[0];
    expect(selected.textContent).toContain('deepseek-flash');
    expect(selected.textContent).toContain('历史');
    expect(selected.dataset.provider).toBeUndefined();
    expect(fake.request.mock.calls.some((c) => c[0] === 'session.start')).toBe(false);
  });

  it('Pi 无法恢复模型时不呈现 unknown 选项且不允许误发', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture', busy: false }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false, isCompacting: false, model: missingModel };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      return {};
    });
    await workbench.reconcile();
    const selected = (document.getElementById('model-select') as HTMLSelectElement).selectedOptions[0];
    expect(selected.textContent).toContain('不可用');
    expect(selected.textContent).not.toContain('unknown');
    expect(selected.dataset.provider).toBeUndefined();
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '不会投到未知模型';
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    expect((document.getElementById('send-button') as HTMLButtonElement).disabled).toBe(true);
  });

  it('Pi 无法恢复模型时保留草稿并拒绝直接提交', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      if (method === 'session.state') return { sessionId: 's1', model: { provider: 'unknown', id: 'unknown', name: 'unknown' } };
      if (method === 'session.thinking_levels') return ['off'];
      return {};
    });
    const input = document.getElementById('prompt') as HTMLTextAreaElement;
    input.value = '未发出的草稿';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(document.getElementById('connection-notice')?.textContent).toContain('没有可用模型'));
    expect(fake.request.mock.calls.some((c) => c[0] === 'session.prompt')).toBe(false);
    expect(input.value).toBe('未发出的草稿');
  });

  it('启动期间的 unknown 状态不能覆盖发送前选定的模型', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      if (method === 'session.state') return { sessionId: 's1', model: missingModel };
      if (method === 'session.set_model') return { provider: 'CPA-Responses', id: 'deepseek-flash', name: 'DeepSeek Flash' };
      if (method === 'session.thinking_levels') return ['off'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      return {};
    });
    const select = document.getElementById('model-select') as HTMLSelectElement;
    const option = new Option('DeepSeek Flash · CPA-Responses', 'CPA-Responses/deepseek-flash');
    option.dataset.provider = 'CPA-Responses'; option.dataset.modelId = 'deepseek-flash'; select.add(option);
    select.value = option.value;
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '用我选的模型';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.prompt')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.set_model', 's1', { provider: 'CPA-Responses', modelId: 'deepseek-flash' }, 30_000);
    expect(select.selectedOptions[0].textContent).not.toContain('unknown');
    const order = fake.request.mock.calls.map((c) => c[0]);
    expect(order.lastIndexOf('session.set_model')).toBeLessThan(order.lastIndexOf('session.prompt'));
  });
});

describe('等待期间切换会话的归属', () => {
  // U03/U13：发送与 command 以前在 await 之后读 this.sessionId，
  // 等待期间切会话会把操作投到新会话上。
  it('启动 worker 期间切会话，消息不投给新会话', async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.start') await gate;
      return undefined;
    });
    (document.getElementById('prompt') as HTMLTextAreaElement).value = 'hello';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.start')).toBe(true));

    // 在 worker 启动等待途中切到另一个会话。
    workbench.selectSession('s2', '/tmp/other', '另一个会话');
    release();
    await waitFor(() => expect(document.getElementById('connection-notice')!.textContent).toContain('未发送'));
    // 关键安全属性：绝不能投到切换后的会话。
    expect(fake.request.mock.calls.some((c) => c[0] === 'session.prompt' && c[1] === 's2')).toBe(false);
    // 输入内容必须保留，用户才能重发。
    expect((document.getElementById('prompt') as HTMLTextAreaElement).value).toBe('hello');
  });

  it('状态刷新期间切会话，command 仍发往原会话', async () => {
    // 让流程真正经过 ensureWorker 内部的状态刷新后仍按发起时的会话归属发送。
    // 自动压缩默认项走的就是 command()（U03 的同一路径）。
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    let gated = false;
    fake.request.mockImplementation(async (method: string) => {
      // command() 内部会先 ensureWorker → session.subscribe → refreshState。
      // 只让「首次」状态刷新挂起，避免后续 selectSession('s2') 自己的刷新也卡住。
      if (method === 'session.state' && !gated) { gated = true; await gate; }
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      return {};
    });
    const box = document.getElementById('default-auto-compaction') as HTMLInputElement;
    box.checked = false;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    // 等它进入 ensureWorker 内部的状态刷新。
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.state')).toBe(true));
    workbench.selectSession('s2', '/tmp/other', '另一个会话');
    release();
    await vi.advanceTimersByTimeAsync(10);
    await waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.set_auto_compaction')).toBe(true));
    // 用户那次改动必须发往发起时的 s1；selectSession('s2') 自身的默认对齐
    // 会另外发一条到 s2（那是新会话的启动对齐，不是用户的这次操作）。
    const userCalls = fake.request.mock.calls.filter((c) => c[0] === 'session.set_auto_compaction' && c[1] === 's1');
    expect(userCalls.length).toBeGreaterThan(0);
    expect(fake.request.mock.calls.every((c) => !(c[0] === 'session.set_name' && c[1] === 's2'))).toBe(true);
  });
});

describe('附件按会话隔离', () => {
  // U01：selectSession 以前不清理全局附件数组，上一会话的图片
  // 会留在新会话里并被发送出去。
  it('切换会话后附件被清空', async () => {
    const input = document.getElementById('attach-input') as HTMLInputElement;
    // 通过 drop 路径挂一个附件（不依赖 FileReader 的真实读取结果）。
    const box = document.getElementById('attachments')!;
    box.hidden = false;
    box.replaceChildren(Object.assign(document.createElement('div'), { className: 'attachment', textContent: '遗留图片' }));
    expect(box.childElementCount).toBe(1);

    workbench.selectSession('s2', '/tmp/other', '另一个会话');
    expect(box.childElementCount).toBe(0);
    expect(box.hidden).toBe(true);
    expect(input).toBeDefined();
  });

  it('同一会话内刷新不清空附件', async () => {
    const box = document.getElementById('attachments')!;
    box.hidden = false;
    box.replaceChildren(Object.assign(document.createElement('div'), { className: 'attachment', textContent: '当前图片' }));
    // 仍在本会话：只做状态刷新，附件必须保留。
    await workbench.reconcile();
    expect(box.childElementCount).toBe(1);
  });
});

describe('自动压缩从 Pi 读回', () => {
  // 自动压缩是 Pi 的实时状态（有读回字段）：状态刷新时把它回显到设置面板。
  // 排队发送方式已改为全局默认，不再从 Pi 读回（见「排队与压缩设置」一组）。
  it('autoCompactionEnabled=false 时设置里的开关取消勾选', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture', busy: false }];
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      if (method === 'session.state') return {
        sessionId: 's1', isStreaming: false, isCompacting: false, thinkingLevel: 'high',
        steeringMode: 'all', followUpMode: 'all', autoCompactionEnabled: false,
      };
      return {};
    });
    const box = document.getElementById('default-auto-compaction') as HTMLInputElement;
    box.checked = true;
    await workbench.reconcile();
    await waitFor(() => expect(box.checked).toBe(false));
  });
});

describe('历史响应的代次守卫', () => {
  // U17/U18：beforeSwap 以前只按 URL 判断会话。代次校验能挡住
  // 「切换前发起、切换后才到达」的响应——这是可在前端判定的一类。
  it('代次递增后到达的旧历史响应被拒绝', async () => {
    const turns = document.getElementById('turns')!;
    // 先在 s1 真正发起一次历史请求，让 pendingHistory 记下 {s1, epochN}。
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    await (workbench as unknown as { refreshHistory(): Promise<void> }).refreshHistory();
    const registered = (workbench as unknown as { pendingHistory: { sessionId: string; epoch: number } }).pendingHistory;
    expect(registered.sessionId).toBe('s1');

    // 代次递增（等价于新建会话时 scope.switchTo 的效果）。
    workbench.scope.switchTo('s1');

    // 旧请求此刻才到达：URL 仍属于 s1，但代次已经过期。
    const stale = new Event('htmx:beforeSwap') as CustomEvent;
    stale.detail = {
      target: turns,
      xhr: { responseURL: `${location.origin}/ui/sessions/s1/history` } as unknown as XMLHttpRequest,
      shouldSwap: true,
    };
    document.dispatchEvent(stale);
    expect(stale.detail.shouldSwap).toBe(false);
  });

  it('当前会话的历史响应被放行', async () => {
    const turns = document.getElementById('turns')!;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    await workbench.reconcile();

    const fresh = new Event('htmx:beforeSwap') as CustomEvent;
    fresh.detail = {
      target: turns,
      xhr: { responseURL: `${location.origin}/ui/sessions/s1/history` } as unknown as XMLHttpRequest,
      shouldSwap: true,
    };
    document.dispatchEvent(fresh);
    expect(fresh.detail.shouldSwap).toBe(true);
  });
});

describe('会话默认是全局偏好', () => {
  // 自动重试/自动压缩/排队方式都是全局默认（本机浏览器），不再按会话隔离：
  // 换会话不改动它们，新会话在启动 worker 时按默认对齐。
  it('修改后写入偏好，换会话保持同一份全局默认', async () => {
    const box = document.getElementById('default-auto-retry') as HTMLInputElement;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await waitFor(() => expect(localStorage.getItem('pi-ui:auto-retry')).toBe('1'));

    // 换到另一个会话：全局默认不受影响（开关保持勾选）。
    workbench.selectSession('s2', '/tmp/b', 'B');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s2'));
    expect(box.checked).toBe(true);
  });
});

describe('思考强度：自动 + 会话记忆', () => {
  // 「自动」不是 Pi 的等级，而是“不覆盖”的语义：绝不能发出 set_thinking。
  it('选择自动时只记住偏好，不发送 set_thinking', async () => {
    const select = document.getElementById('thinking-select') as HTMLSelectElement;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    await waitFor(() => expect(select.options.length).toBeGreaterThan(1));
    expect(select.value).toBe('');
    select.value = '';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.thinking_levels', 's1', undefined, 30_000));
    const calls = fake.request.mock.calls.filter(([method]) => method === 'session.set_thinking');
    expect(calls).toHaveLength(0);
  });

  it('选择具体等级时会发送 set_thinking 并按会话记住', async () => {
    const select = document.getElementById('thinking-select') as HTMLSelectElement;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(select.options.length).toBeGreaterThan(1));
    select.value = 'high';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_thinking', 's1', { level: 'high' }, 30_000));
    // 切到别的会话再切回：选择必须保留，不能悄悄回到“自动”。
    workbench.selectSession('s2', '/tmp/b', 'B');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s2'));
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    expect(select.value).toBe('high');
  });

  it('新会话默认显示自动', async () => {
    const select = document.getElementById('thinking-select') as HTMLSelectElement;
    workbench.selectSession('s9', '/tmp/c', 'C');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s9'));
    expect(select.value).toBe('');
    expect(select.selectedOptions[0]?.textContent).toBe('自动');
  });
});

describe('上下文用量来自 Pi 统计', () => {
  it('有 contextWindow 时显示占比与 token 数', async () => {
    const node = document.getElementById('context-usage')!;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.stats') return { totalMessages: 3, cost: 0.001, contextUsage: { tokens: 32000, contextWindow: 65536, percent: 48.8 } };
      return undefined;
    });
    emit('agent_settled');
    await waitFor(() => expect(node.hidden).toBe(false));
    expect(node.textContent).toBe('49% · 32k / 66k');
    expect(node.title).toContain('48.8%');
  });

  it('压缩后 percent 为 null 时显示未知而不是 0', async () => {
    const node = document.getElementById('context-usage')!;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.stats') return { contextUsage: { tokens: null, contextWindow: 65536, percent: null } };
      return undefined;
    });
    emit('agent_settled');
    await waitFor(() => expect(node.hidden).toBe(false));
    expect(node.textContent).toBe('? / 66k');
    expect(node.title).toContain('压缩后');
  });

  it('Pi 统计失败时仍显示已知的上下文窗口并标注未知', async () => {
    const node = document.getElementById('context-usage')!;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false, model: { provider: 'CPA-Responses', id: 'deepseek-flash', name: 'DeepSeek Flash', contextWindow: 65536 } };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.stats') throw new Error('Pi 无法汇总本次会话的用量统计（可能包含失败的回合）');
      return {};
    });
    emit('agent_settled');
    await waitFor(() => expect(node.hidden).toBe(false));
    expect(node.textContent).toBe('? / 66k');
    expect(node.title).toContain('统计不可用');
  });

  it('没有 contextUsage 时保持隐藏', async () => {
    const node = document.getElementById('context-usage')!;
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.stats') return { totalMessages: 1 };
      return undefined;
    });
    emit('agent_settled');
    await waitFor(() => expect(node.hidden).toBe(true));
  });
});

// stubHost 构造顶栏面板需要的最小宿主，用来直接验证预设切换策略，
// 不依赖 DOM 运行状态（假定时器下状态会被 reconcile 反复刷写）。
function stubHost(overrides: { busy?: boolean } = {}) {
  const state = { stopCalls: [] as unknown[], startCalls: [] as unknown[], notices: [] as string[], presetBySession: new Map<string, string>() };
  const host = {
    stopCalls: state.stopCalls,
    startCalls: state.startCalls,
    notices: state.notices,
    presetBySession: () => state.presetBySession,
    sessionId: () => 's1',
    cwd: () => '/tmp/a',
    busy: () => overrides.busy ?? false,
    modelLabel: () => 'deepseek-flash · CPA-Responses',
    contextWindow: () => 65536,
    setContextWindow: () => {},
    thinking: () => '',
    preset: () => (document.getElementById('tool-preset-quick') as HTMLSelectElement).value,
    setPreset: (value: string) => { const node = document.getElementById('tool-preset-quick') as HTMLSelectElement | null; if (node) node.value = value; },
    presetKey: () => 's1',
    request: async (method: string, params?: unknown) => {
      if (method === 'session.stop') { state.stopCalls.push(params); return {}; }
      if (method === 'session.start') { state.startCalls.push(params); return { sessionId: 's1', cwd: '/tmp/a' }; }
      return {};
    },
    notify: (message: string) => state.notices.push(message),
    fail: (error: unknown) => state.notices.push(String(error)),
    refreshState: async () => {},
  };
  return host;
}

// startCalls 收集 session.start 的参数，避免被 cwd 回写之类的细节绑死。
function startCalls(): Record<string, unknown>[] {
  return fake.request.mock.calls
    .filter(([method]) => method === 'session.start')
    .map(([, , params]) => (params ?? {}) as Record<string, unknown>);
}

describe('工具预设切换需要重启 worker', () => {
  it('空闲时直接停止并按新预设重启', async () => {
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    const select = document.getElementById('tool-preset-quick') as HTMLSelectElement;
    select.value = 'read-only';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await waitFor(() => expect(startCalls().some((params) => params.toolPreset === 'read-only')).toBe(true));
    expect((document.getElementById('tool-preset-quick') as HTMLSelectElement).value).toBe('read-only');
  });

  it('运行中切换必须先确认，取消则回滚', async () => {
    const topbar = await import('../../src/modules/topbar');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const host = stubHost({ busy: true });
    (document.getElementById('tool-preset-quick') as HTMLSelectElement).value = 'chat-only';
    await topbar.changeToolPreset(host);
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0]?.[0]).toContain('重启');
    expect(host.stopCalls).toHaveLength(0);
    expect(host.startCalls).toHaveLength(0);
    // 取消后必须回到上一个值，不能停在用户刚点的那个。
    expect((document.getElementById('tool-preset-quick') as HTMLSelectElement).value).toBe('default');
    confirm.mockRestore();
  });

  it('运行中确认后会停掉并按新预设重启', async () => {
    const topbar = await import('../../src/modules/topbar');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const host = stubHost({ busy: true });
    (document.getElementById('tool-preset-quick') as HTMLSelectElement).value = 'full';
    await topbar.changeToolPreset(host);
    expect(host.stopCalls).toEqual([{ force: false }]);
    expect(host.startCalls).toEqual([{ cwd: '/tmp/a', toolPreset: 'full' }]);
    expect(host.presetBySession().get('s1')).toBe('full');
    confirm.mockRestore();
  });

  it('预设按会话隔离', async () => {
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    const select = document.getElementById('tool-preset-quick') as HTMLSelectElement;
    select.value = 'full';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await waitFor(() => expect(startCalls().some((params) => params.toolPreset === 'full')).toBe(true));
    workbench.selectSession('s2', '/tmp/b', 'B');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s2'));
    expect(select.value).toBe('default');
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    expect(select.value).toBe('full');
  });
});

describe('顶栏功能面板', () => {
  it('同一时间只打开一个面板', async () => {
    const title = document.getElementById('panel-title')!;
    const system = document.getElementById('panel-system')!;
    const tools = document.getElementById('panel-tools')!;
    document.querySelector('[data-action="panel-title"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(title.hidden).toBe(false));
    document.querySelector('[data-action="panel-tools"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(tools.hidden).toBe(false));
    expect(title.hidden).toBe(true);
    expect(system.hidden).toBe(true);
    // 关闭按钮只关自己那一个。
    document.querySelector('[data-action="panel-tools-close"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(tools.hidden).toBe(true));
  });

  // 工具面板本体由桥渲染（/ui/tools）：这里只锁前端职责——把会话 ID 交给请求、
  // 触发一次刷新。列表内容（实际暴露给模型的工具、受预设控制）由桥的片段测试覆盖。
  it('工具面板把会话 ID 交给片段请求', async () => {
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    document.querySelector('[data-action="panel-tools"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(document.getElementById('panel-tools')!.hidden).toBe(false));
    expect((document.getElementById('tools-session') as HTMLInputElement).value).toBe('s1');
    expect(vi.mocked(window.htmx.trigger).mock.calls.some((call) => call[1] === 'tools-refresh')).toBe(true);
    expect(document.getElementById('tools-body')).not.toBeNull();
  });

  it('会话信息面板承载会话名称与工作目录', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/tmp/a' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      return {};
    });
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    document.querySelector('[data-action="panel-info"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(document.getElementById('panel-info')!.hidden).toBe(false));
    // 详情整体由桥渲染（/ui/stats）：前端只带上会话 ID 触发刷新。
    expect((document.getElementById('stats-session') as HTMLInputElement).value).toBe('s1');
    expect(vi.mocked(window.htmx.trigger).mock.calls.some((call) => call[1] === 'stats-refresh')).toBe(true);
    // 顶栏本身不再显示会话标题：标题只在侧栏与信息面板里。
    expect(document.querySelector('.topbar #session-title')).toBeNull();
    expect(document.querySelector('.session-heading')).toBeNull();
  });

  it('完整历史在新标签页打开导出的 HTML', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.export_html') return { path: '/exports/session-abc.html' };
      return {};
    });
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    document.querySelector('[data-action="full-history"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(open).toHaveBeenCalled());
    expect(open.mock.calls[0]?.[0]).toBe('/ui/exports/session-abc.html?inline=1');
    open.mockRestore();
  });

  it('系统面板把会话 ID 交给片段请求', async () => {
    workbench.selectSession('s1', '/tmp/a', 'A');
    await waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture' }];
      if (method === 'session.state') return { sessionId: 's1', isStreaming: false };
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.stats') return { contextUsage: { tokens: 1024, contextWindow: 65536, percent: 1.6 } };
      return undefined;
    });
    emit('agent_settled');
    await waitFor(() => expect(document.getElementById('context-usage')!.hidden).toBe(false));
    document.querySelector('[data-action="panel-system"]')!.dispatchEvent(new Event('click', { bubbles: true }));
    await waitFor(() => expect(document.getElementById('panel-system')!.hidden).toBe(false));
    // 提示词内容由桥渲染；前端只负责带上会话 ID 与触发刷新。
    expect((document.getElementById('system-session') as HTMLInputElement).value).toBe('s1');
    expect(vi.mocked(window.htmx.trigger).mock.calls.some((call) => call[1] === 'system-refresh')).toBe(true);
  });
});

describe('模型配置的两级树与字段表单', () => {
  const open = async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await waitFor(() => expect(document.querySelectorAll('#models-tree-body [data-models-provider]').length).toBeGreaterThan(0));
  };
  const clickTree = (selector: string) => {
    document.querySelector<HTMLElement>(selector)!.click();
  };

  it('树按 provider → model 两级渲染，模型行带推理标记', async () => {
    await open();
    const rows = [...document.querySelectorAll<HTMLElement>('#models-tree-body [data-models-provider]')];
    // provider 行不带 data-models-model，模型行带下标。
    const providers = rows.filter((r) => r.dataset.modelsModel === undefined);
    const models = rows.filter((r) => r.dataset.modelsModel !== undefined);
    expect(providers.map((r) => r.textContent)).toContain('cpa');
    expect(models.map((r) => r.textContent)).toEqual(['m1T', 'm2']);
    // 模型行必须缩进，否则两级看起来是平级的。
    expect(models[0].className).toContain('models-indent');
    expect(document.querySelector('[data-models-add-model]')?.textContent).toContain('+ 模型');
    expect(document.querySelector('[data-models-add-provider]')?.textContent).toContain('+ 供应商');
  });

  it('点 provider 行把字段填进表单', async () => {
    await open();
    clickTree('#models-tree-body [data-models-provider]:not([data-models-model])');
    expect((document.getElementById('mp-name') as HTMLInputElement).value).toBe('cpa');
    expect((document.getElementById('mp-base') as HTMLInputElement).value).toBe('https://example.com/v1');
    // 密钥读回来必须是打码值，页面拿不到真值。
    expect((document.getElementById('mp-key') as HTMLInputElement).value).toBe('***');
    expect((document.getElementById('mp-api') as HTMLSelectElement).value).toBe('openai-completions');
    expect(document.querySelector('[data-models-panel=provider]')?.hidden).toBe(false);
  });

  it('点模型行填模型表单，能力复选框反映记录', async () => {
    await open();
    clickTree('#models-tree-body [data-models-model="0"]');
    expect((document.getElementById('mm-id') as HTMLInputElement).value).toBe('m1');
    expect((document.getElementById('mm-name') as HTMLInputElement).value).toBe('旧名字');
    expect((document.getElementById('mm-reasoning') as HTMLInputElement).checked).toBe(true);
    expect((document.getElementById('mm-image') as HTMLInputElement).checked).toBe(false);
    // 思考等级映射必须把七个等级都渲染出来，否则用户看不到有哪些可选。
    expect(document.querySelectorAll('#mm-thinking [data-thinking-level]').length).toBe(7);
  });

  it('思考等级映射按三态回显：有值、null、缺键各不相同', async () => {
    await open();
    clickTree('#models-tree-body [data-models-model="0"]');
    const row = (level: string) => document.querySelector<HTMLElement>(`#mm-thinking [data-thinking-level="${level}"]`)!;
    // 有值 → string，输入框带值
    expect(row('high').dataset.state).toBe('string');
    expect(row('high').querySelector('input')!.value).toBe('high');
    // null → 禁用态
    expect(row('low').dataset.state).toBe('null');
    expect(row('low').querySelector('input')!.value).toBe('');
    // 缺键 → 默认态
    expect(row('medium').dataset.state).toBe('omit');
  });

  it('思考等级映射写出时保留 null 与省略的区别', async () => {
    await open();
    clickTree('#models-tree-body [data-models-model="0"]');
    // 把 off 从自定义改成禁用，把 high 改成默认（删键），medium 设成自定义
    row_click('off', 'null');
    row_click('high', 'omit');
    row_click('medium', 'string');
    const writes: unknown[] = [];
    vi.mocked(fake.request).mockImplementation(async (method: string, _id: string, params?: unknown) => {
      if (method === 'config.models.raw') return { providers: { cpa: { api: 'openai-completions', baseUrl: 'https://example.com/v1', apiKey: '***', models: [{ id: 'm1', name: '旧名字', reasoning: true }, { id: 'm2', name: '第二个' }] } } };
      if (method === 'config.models.write') { writes.push(params); return { written: true }; }
      return {};
    });
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    await waitFor(() => expect(writes.length).toBe(1));
    const map = (writes[0] as { config: { providers: { cpa: { models: Array<{ thinkingLevelMap?: Record<string, string | null> }> } } } }).config.providers.cpa.models[0].thinkingLevelMap!;
    // null 必须写出去，不能被转成空串或删掉。
    expect(map.off).toBeNull();
    expect('high' in map).toBe(false);
    expect(map.medium).toBe('medium');
    // 没动过的 low 仍是 null。
    expect(map.low).toBeNull();
  });

  it('思考等级切到默认或禁用时清掉输入框里的旧值', async () => {
    await open();
    clickTree('#models-tree-body [data-models-model="0"]');
    const row = document.querySelector<HTMLElement>('#mm-thinking [data-thinking-level="high"]')!;
    expect(row.querySelector('input')!.value).toBe('high');
    row.querySelector<HTMLElement>('[data-tl="omit"]')!.click();
    // 值必须清掉：留着会让人以为它还生效，而保存时这一行不会被读。
    expect(row.querySelector('input')!.value).toBe('');
    expect(row.dataset.state).toBe('omit');
    // 再切回自定义，自动填上等级名本身。
    row.querySelector<HTMLElement>('[data-tl="string"]')!.click();
    expect(row.querySelector('input')!.value).toBe('high');
    expect(row.dataset.state).toBe('string');
  });

  function row_click(level: string, state: string): void {
    const row = document.querySelector<HTMLElement>(`#mm-thinking [data-thinking-level="${level}"]`)!;
    row.querySelector<HTMLElement>(`[data-tl="${state}"]`)!.click();
  }

  it('改表单后保存，写回的是整份文档而不是只有改过的字段', async () => {
    await open();
    clickTree('#models-tree-body [data-models-model="0"]');
    (document.getElementById('mm-name') as HTMLInputElement).value = '新名字';
    const writes: unknown[] = [];
    vi.mocked(fake.request).mockImplementation(async (method: string, _id: string, params?: unknown) => {
      if (method === 'config.models.raw') return { providers: { cpa: { api: 'openai-completions', baseUrl: 'https://example.com/v1', apiKey: '***', models: [{ id: 'm1', name: '新名字', reasoning: true }, { id: 'm2', name: '第二个' }] } } };
      if (method === 'config.models.write') { writes.push(params); return { written: true }; }
      return {};
    });
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    await waitFor(() => expect(writes.length).toBe(1));
    const config = (writes[0] as { config: { providers: { cpa: { models: Array<{ id: string; name: string }> } } } }).config;
    expect(config.providers.cpa.models[0].name).toBe('新名字');
    // 没动过的第二个模型必须原样保留：只提交改过的字段会把其余模型删掉。
    expect(config.providers.cpa.models[1].name).toBe('第二个');
  });

  it('密钥显示开关切换 input 类型', async () => {
    await open();
    clickTree('#models-tree-body [data-models-provider]:not([data-models-model])');
    const input = document.getElementById('mp-key') as HTMLInputElement;
    const button = document.createElement('button');
    expect(input.type).toBe('password');
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-toggle-secret', button);
    expect(input.type).toBe('text');
    expect(button.textContent).toBe('隐藏');
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-toggle-secret', button);
    expect(input.type).toBe('password');
    expect(button.textContent).toBe('显示');
  });
});

describe('事件游标的 epoch 归属', () => {
  // U16：EventCursor 以前接受任意新 epoch 并把 seq 归零，于是上一个 worker
  // 的延迟帧能把游标切回去，旧事件被当成新事件应用。
  it('epoch 只由订阅确认切换，事件流里的旧 epoch 被丢弃', async () => {
    fake.request.mockImplementation(async (method: string) => method === 'session.subscribe' ? { subscribed: true, epoch: 'live-1', seq: 5 } : {});
    const internal = workbench as unknown as { subscribed: string; subscribe(): Promise<void>; cursor: { epoch: string; seq: number } };
    // 初始化流程可能已经订阅过同一会话，直接调 subscribe 会被幂等短路。
    internal.subscribed = '';
    await internal.subscribe();
    const cursor = internal.cursor;
    expect(cursor.epoch).toBe('live-1');
    expect(cursor.seq).toBe(5);
    // 上一个 worker 的延迟帧：序号更大也不能接受。
    emitRaw('stale', 99, { type: 'agent_start' });
    expect(cursor.epoch).toBe('live-1');
    expect(cursor.seq).toBe(5);
    // 当前 epoch 的正常帧照常推进。
    emitRaw('live-1', 6, { type: 'agent_start' });
    expect(cursor.seq).toBe(6);
    expect(cursor.epoch).toBe('live-1');
  });
});

describe('实时流重同步与迟到回执的会话归属', () => {
  // U16 残留：事件帧已挡住旧 epoch，但订阅确认此前没有会话代次守卫——
  // A 会话的确认迟到回来时，仍会把新会话的游标改成 A 的 epoch。
  it('迟到的订阅确认不改写新会话的游标', async () => {
    const impl = fake.request.getMockImplementation()!;
    let release: (() => void) | undefined;
    fake.request.mockImplementation(async (method: string, sessionId: string, params?: unknown, timeout?: number) => {
      if (method === 'session.subscribe' && sessionId === 's1') {
        await new Promise<void>((resolve) => { release = resolve; });
        return { subscribed: true, epoch: 'e-stale', seq: 40 };
      }
      return impl(method, sessionId, params, timeout);
    });
    const internal = workbench as unknown as { subscribed: string; subscribe(): Promise<void>; cursor: { epoch: string; seq: number }; selectSession(id: string, cwd: string, title: string, push?: boolean, entryId?: string, persisted?: boolean): void };
    internal.subscribed = '';
    const stale = internal.subscribe();
    internal.selectSession('s2', '/w2', '另一个会话', false);
    release!();
    await stale;
    expect(internal.cursor.epoch).not.toBe('e-stale');
  });

  // U15：桥明确要求重同步（补发超时）时只重订不够，中间事件已丢。
  it('订阅被关闭且要求重同步时重读历史', async () => {
    const internal = workbench as unknown as { subscribed: string; diskSession: boolean };
    internal.diskSession = true; internal.subscribed = 's1';
    const ajax = window.htmx.ajax as unknown as ReturnType<typeof vi.fn>;
    ajax.mockClear();
    fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version: 1, kind: 'control', event: 'bridge.subscription_closed', sessionId: 's1', data: { resyncRequired: true } } }));
    await waitFor(() => expect(ajax).toHaveBeenCalledWith('get', expect.stringContaining('/ui/sessions/s1/history'), expect.anything()));
  });

  // U15：事件因体积被省略时流已断开，等下一次 settled 会一直缺内容。
  it('事件被省略时立即重读历史', async () => {
    const internal = workbench as unknown as { diskSession: boolean };
    internal.diskSession = true;
    const ajax = window.htmx.ajax as unknown as ReturnType<typeof vi.fn>;
    ajax.mockClear();
    emitNamed('bridge.event_omitted', { type: 'pi.event', reason: '事件体积超过上限', resyncRequired: true });
    await waitFor(() => expect(ajax).toHaveBeenCalledWith('get', expect.stringContaining('/ui/sessions/s1/history'), expect.anything()));
    expect(document.getElementById('connection-notice')!.textContent).toContain('体积');
  });

  // 用户实报：点进会话后两段内容快速来回切换、刷新即好。根因之一是
  // 「订阅被反复摘除（慢消费者）→ 立即重读+重订 → 渲染忙 → 又被摘除」
  // 的高频循环，每一次循环都会整体换入一次 #turns。重读必须合并到
  // 冷却窗口：窗口内重复的请求只记积压，窗口结束补一轮。
  it('订阅反复被摘除时的重读合并到冷却窗口', async () => {
    const internal = workbench as unknown as { diskSession: boolean };
    internal.diskSession = true;
    const ajax = window.htmx.ajax as unknown as ReturnType<typeof vi.fn>;
    ajax.mockClear();
    const historyCalls = () => ajax.mock.calls.filter((call) => String(call[1]).includes('/history')).length;
    // 首次：立即重读。
    emitNamed('bridge.event_omitted', { type: 'pi.event', reason: '事件体积超过上限', resyncRequired: true });
    await waitFor(() => expect(historyCalls()).toBe(1));
    // 冷却窗内的两次重复摘除：合并为一次积压，不马上再换历史。
    emitNamed('bridge.event_omitted', { type: 'pi.event', reason: '事件体积超过上限', resyncRequired: true });
    emitNamed('bridge.event_omitted', { type: 'pi.event', reason: '事件体积超过上限', resyncRequired: true });
    await vi.advanceTimersByTimeAsync(500);
    expect(historyCalls()).toBe(1);
    // 窗口结束：积压补一轮（仍然是每窗口至多一次）。
    await vi.advanceTimersByTimeAsync(1200);
    expect(historyCalls()).toBe(2);
  });

  // U18：回执等待期间切会话，迟到的错误不得写进新会话。
  it('切会话后迟到的回执错误不写进新会话', async () => {
    pending = ['dialog-1']; busy = true; emit('extension_ui_request', { id: 'dialog-1', method: 'input', title: '请输入' });
    await waitFor(() => expect(document.querySelector('#ext-dialog-slot input')).not.toBeNull());
    const impl = fake.request.getMockImplementation()!;
    let rejectResponse: ((error: Error) => void) | undefined;
    fake.request.mockImplementation(async (method: string, sessionId: string, params?: unknown, timeout?: number) => {
      if (method === 'session.ui_response') { await new Promise<void>((_, reject) => { rejectResponse = reject; }); }
      return impl(method, sessionId, params, timeout);
    });
    const internal = workbench as unknown as { answerDialog(form: HTMLFormElement, button: HTMLButtonElement | null): Promise<void>; selectSession(id: string, cwd: string, title: string, push?: boolean, entryId?: string, persisted?: boolean): void };
    const form = document.querySelector<HTMLFormElement>('#ext-dialog-slot form')!;
    const submit = form.querySelector<HTMLButtonElement>('button[type=submit]')!;
    const answered = internal.answerDialog(form, submit);
    internal.selectSession('s2', '/w2', '另一个会话', false);
    rejectResponse!(new Error('回执失败'));
    await answered;
    expect(document.getElementById('connection-notice')!.textContent).toBe('');
  });

  // B76：导出是磁盘投影，不经过 ensureWorker——只看历史不该拉起 Pi 进程。
  it('导出不启动工作进程', async () => {
    vi.stubGlobal('location', { assign: vi.fn() });
    const internal = workbench as unknown as { action(action: string, button: HTMLElement): Promise<void> };
    const starts = () => fake.request.mock.calls.filter(([m]) => m === 'session.start').length;
    const before = starts();
    await internal.action('export', document.createElement('button'));
    // 导出走动态分块（session-actions），等它完成；期间不得拉起工作进程。
    await waitFor(() => expect(fake.request.mock.calls.some(([m]) => m === 'session.export_html')).toBe(true));
    expect(starts()).toBe(before);
    expect(fake.request).toHaveBeenCalledWith('session.export_html', 's1', { fileName: 'session-s1.html' }, 120_000);
  });

  // R01：分支导航的监听绑在 signal 上；不传 signal 时销毁工作台
  // 留着监听，已卸载的 DOM 仍会被迟到响应改写。
  it('销毁工作台后分支面板的监听随之中止', async () => {
    document.body.insertAdjacentHTML('beforeend', '<div id=branch-status></div><div id=branch-body></div>');
    const internal = workbench as unknown as { action(action: string, button: HTMLElement): Promise<void>; branch?: unknown };
    await internal.action('branch', document.createElement('button'));
    expect(internal.branch).toBeTruthy();
    const status = document.getElementById('branch-status')!;
    expect(status.textContent).toContain('正在读取会话树');
    workbench.dispose();
    document.dispatchEvent(new CustomEvent('htmx:afterSwap', { detail: { target: { id: 'branch-body' } } }));
    expect(status.textContent).toContain('正在读取会话树');
  });
});

describe('扩展状态行快照', () => {
  // B36：桥把 setStatus 记在 worker 上；前端在订阅确认后补齐。
  // 老实现是桥侧的全局表且只在转发路径更新，页面加载时拿不到已发生的状态行。
  it('订阅确认后补齐扩展状态行', async () => {
    const impl = fake.request.getMockImplementation()!;
    fake.request.mockImplementation(async (method: string, sessionId: string, params?: unknown, timeout?: number) => {
      if (method === 'session.ext_status') return { epoch: 'e1', statuses: { mc: 'mc: 3 (1%) · idle' } };
      return impl(method, sessionId, params, timeout);
    });
    const internal = workbench as unknown as { subscribed: string; statuses: Map<string, string>; subscribe(): Promise<void> };
    internal.subscribed = '';
    await internal.subscribe();
    await waitFor(() => expect(internal.statuses.get('mc')).toBe('mc: 3 (1%) · idle'));
    expect(document.getElementById('ext-status-slot')!.textContent).toContain('mc: 3');
  });

  it('快照不覆盖已收到的新状态', async () => {
    const internal = workbench as unknown as { subscribed: string; statuses: Map<string, string>; subscribe(): Promise<void> };
    internal.subscribed = '';
    // 先收到一条更新的 WS 增量（同一 key）。
    emit('extension_ui_request', { method: 'setStatus', statusKey: 'mc', statusText: 'mc: 20 (9%) · running' });
    const impl = fake.request.getMockImplementation()!;
    fake.request.mockImplementation(async (method: string, sessionId: string, params?: unknown, timeout?: number) => {
      if (method === 'session.ext_status') return { epoch: 'e1', statuses: { mc: 'mc: 3 (1%) · idle', extra: '额外行' } };
      return impl(method, sessionId, params, timeout);
    });
    await internal.subscribe();
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.ext_status', 's1', undefined, 30_000));
    // 较旧的快照只能补缺，不能把已更新的值拉回去；缺的 key 仍然补上。
    expect(internal.statuses.get('mc')).toBe('mc: 20 (9%) · running');
    await waitFor(() => expect(internal.statuses.get('extra')).toBe('额外行'));
  });
});

describe('命令等待上限', () => {
  it('长任务按桥的标注放宽，其余仍是默认', async () => {
    // 桥 protocol/methods.go 给压缩与用户 bash 标注了 5 分钟，导出 2 分钟；
    // 前端先放弃只会让用户以为失败（B66）。这里锁定两侧的上限对应关系。
    // 用「按方法找调用」而不是 at(-1)：ensureWorker 会顺带发 session.stats，
    // 最后一条未必是目标方法。
    const timeouts = (method: string) => fake.request.mock.calls.filter((c) => c[0] === method).map((c) => c[3]);
    const call = (method: string) => (workbench as unknown as { command(m: string): Promise<unknown> }).command(method);
    await call('session.compact');
    expect(timeouts('session.compact')).toContain(300_000);
    await call('session.bash');
    expect(timeouts('session.bash')).toContain(300_000);
    await call('session.export_html');
    expect(timeouts('session.export_html')).toContain(120_000);
    await call('session.state');
    expect(timeouts('session.state')).toContain(30_000);
  });
});

// 直接打开 ?session=… 时不经过 selectSession，侧栏文件树要自己跟上。
// 这是 D 批次把文件树移进侧栏时留下的缺口：刷新页面后文件区停在空路径，
// 服务端回退到第一个工作区根，列出的是别的目录。
describe('启动恢复会话时同步文件树', () => {
  // 直接打开 ?session=… 不经过 selectSession，侧栏文件树要自己跟上。
  // 这是 D 批次把文件树移进侧栏时留下的缺口：刷新页面后文件区停在空路径，
  // 服务端回退到第一个工作区根，列出的是别的目录。
  const marks = () => workbench as unknown as { cwd: string; sessionId: string; markSelected(): void };

  it('列表刷新后把选中会话的目录推给文件区', () => {
    const internal = marks();
    internal.cwd = '';
    internal.sessionId = 's1';
    document.body.insertAdjacentHTML('beforeend', '<a class="session-item" data-session="s1" data-cwd="/repo/one" data-title="一"></a>');
    const filesPath = document.getElementById('files-path') as HTMLInputElement;
    filesPath.value = '';
    internal.markSelected();
    expect(filesPath.value).toBe('/repo/one');
  });

  it('不覆盖用户已经浏览到的目录', () => {
    const internal = marks();
    internal.cwd = '/repo/one';
    internal.sessionId = 's1';
    document.body.insertAdjacentHTML('beforeend', '<a class="session-item" data-session="s1" data-cwd="/repo/one"></a>');
    const filesPath = document.getElementById('files-path') as HTMLInputElement;
    filesPath.value = '/repo/elsewhere';
    internal.markSelected();
    expect(filesPath.value).toBe('/repo/elsewhere');
  });
});

// 发送路径的两处防回归：都被真实观察触发过。
// 1) 重复提交：此前是静默 return，用户无法区分「还在发」与「点击没反应」，
//    从外部也无从诊断（实测中一次偶发点击无效果，事后查不出原因）。
// 2) sending 残留：置真后到 finally 之间的取值若抛错，按钮会永久置灰。
describe('发送中的重复提交与失败恢复', () => {
  it('发送未完成时再次提交只提示，不重复投递', async () => {
    const form = document.querySelector<HTMLFormElement>('#composer')!;
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '只此一条';
    let release: (value: unknown) => void = () => {};
    fake.request.mockImplementationOnce(() => new Promise((resolve) => { release = resolve; }));
    form.requestSubmit();
    await waitFor(() => expect((document.getElementById('send-button') as HTMLButtonElement).disabled).toBe(true));
    form.requestSubmit();
    await waitFor(() => {
      expect(document.querySelector('#toast-root .toast-warning')?.textContent ?? '').toContain('仍在发送中');
    });
    // 第一次仍卡在启动阶段：没有第二条 prompt 被投递。
    expect(fake.request.mock.calls.filter((call) => call[0] === 'session.prompt')).toHaveLength(0);
    release({ sessionId: 's1', cwd: '/fixture', epoch: 'e1', pid: 1, status: 'idle', busy: false, seq: 0 });
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '只此一条' }, 30_000));
  });

  it('一次发送失败后 sending 不残留，可以立即重发', async () => {
    const form = document.querySelector<HTMLFormElement>('#composer')!;
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '第一条';
    fake.request.mockRejectedValueOnce(new Error('模拟的启动失败'));
    form.requestSubmit();
    await waitFor(() => expect(document.getElementById('connection-notice')!.textContent).toContain('模拟的启动失败'));
    expect((document.getElementById('send-button') as HTMLButtonElement).disabled).toBe(false);
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '第二条';
    document.getElementById('prompt')!.dispatchEvent(new Event('input', { bubbles: true }));
    form.requestSubmit();
    await waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '第二条' }, 30_000));
  });
});
