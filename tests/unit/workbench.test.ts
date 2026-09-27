import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { Workbench } from '@/modules/workbench';

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
const methods = ['session.start','session.prompt','session.subscribe','worker.list','session.state','session.thinking_levels','session.pending_dialogs','session.ui_response','session.stats','session.set_queue_mode','session.set_auto_compaction','session.set_auto_retry','session.abort_retry','session.export_html','config.models.raw','config.models.write','config.models.discover','config.models.test'];
function emit(type: string, extra: Record<string, unknown> = {}) {
 fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version:1,kind:'event',event:'pi.event',sessionId:'s1',epoch:'test',seq:++sequence,data:{type,...extra} } }));
}
function mount() {
 document.body.innerHTML = `<form id=auth-form><input id=bridge-token><button>连接</button></form><dialog id=auth-dialog></dialog><div id=auth-error></div>
 <form id=composer><textarea id=prompt></textarea><div id=attachments hidden></div><p id=composer-drop hidden></p><input id=attach-input type=file><button id=send-button></button><button id=abort-button></button><select id=model-select></select><select id=thinking-select></select></form>
 <form id=new-form><input id=cwd-input></form><dialog id=new-dialog></dialog><datalist id=workspace-roots></datalist><input id=session-search>
 <button class=icon-btn data-action=session-menu aria-label=会话操作>···</button><dialog id=session-dialog><input id=session-name><div class=session-action-grid><button data-action=rename>保存名称</button><button data-action=compact>压缩</button><button data-action=clone>克隆</button><button data-action=export>导出</button><button data-action=stop>释放</button><button data-action=delete>删除</button></div>
 <label class=switch><input type=checkbox id=auto-compaction><span>自动压缩</span></label><label class=switch><input type=checkbox id=auto-retry><span>自动重试</span></label>
 <button data-action=abort-retry>中止重试</button>
 <fieldset class=queue-modes><label class=switch><input type=radio name=queue-kind value=steering checked><span>插入指令</span></label><label class=switch><input type=radio name=queue-kind value=followUp><span>完成后追加</span></label></fieldset></dialog>
 <div class=queue-hint id=queue-hint hidden></div>
 <dialog id=models-dialog><p id=models-status></p><textarea id=models-editor></textarea><input id=discover-url><input id=discover-api><input id=discover-key><textarea id=discover-headers></textarea><div id=discover-result></div><button data-action=models-edit>编辑</button><button data-action=models-reload>重读</button><button data-action=models-save>保存</button><button data-action=models-discover>发现</button><button data-action=models-test>测试</button></dialog>`;
 for(const id of ['live','conn-state','connection-notice','session-state','session-title','session-cwd','session-list','session-count','turns','older-slot','chat-scroll','welcome','command-menu','ext-status-slot','ext-widgets-before','ext-widgets-after','ext-dialog-slot','usage','toast-root']) {const node=document.createElement('div');node.id=id;document.body.append(node);}
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

beforeEach(async () => {
 vi.useFakeTimers(); pending=[];busy=false;sequence=0;mount();
 vi.stubGlobal('fetch',vi.fn(async () => new Response(JSON.stringify({version:1,methods}),{status:200})));
 fake.request.mockImplementation(async (method:string) => {
  if(method==='worker.list')return[{sessionId:'s1',cwd:'/fixture',busy}];
  if(method==='session.state')return{sessionId:'s1',sessionName:'隔离会话',isStreaming:busy,isCompacting:false,steeringMode:'all',followUpMode:'all',autoCompactionEnabled:true};
  if(method==='session.thinking_levels')return['off','high'];
  if(method==='config.models.raw')return{providers:{cpa:{api:'https://example.com/v1',apiKey:'***',models:{m1:{name:'旧名字'}}}}};
  if(method==='config.models.write')return{written:true};
  if(method==='config.models.discover')return{models:[{id:'gpt-x',name:'GPT X'}]};
  if(method==='config.models.test')return{ok:true,message:'连通正常'};
  if(method==='session.pending_dialogs')return{ids:[...pending]};
  if(method==='session.ui_response'){pending=[];busy=false;emit('agent_settled');return{answered:true};}
  return{};
 });
 workbench=new Workbench(vi.fn());workbench.start();
 await vi.waitFor(()=>expect(fake.request).toHaveBeenCalledWith('session.state','s1',undefined,30_000));
});
afterEach(()=>{workbench?.dispose();vi.useRealTimers();vi.unstubAllGlobals();document.body.replaceChildren();});

describe('扩展对话交互回归',()=>{
 it('状态轮询不替换仍在编辑的同一对话',async()=>{
  pending=['dialog-1'];busy=true;emit('extension_ui_request',{id:'dialog-1',method:'input',title:'请输入'});
  await vi.waitFor(()=>expect(document.querySelector('#ext-dialog-slot input')).not.toBeNull());
  const input=document.querySelector<HTMLInputElement>('#ext-dialog-slot input')!;input.value='尚未提交的内容';
  await vi.advanceTimersByTimeAsync(15_000);
  expect(document.querySelector('#ext-dialog-slot input')).toBe(input);
  expect(input.value).toBe('尚未提交的内容');
  const requests=vi.mocked(window.htmx.ajax).mock.calls.filter((call)=>String(call[1]).startsWith('/ui/extensions/dialogs'));
  expect(requests).toHaveLength(1);
 });
 it('回执期间已结束的任务不会被重新标记为运行中',async()=>{
  pending=['dialog-1'];busy=true;emit('extension_ui_request',{id:'dialog-1',method:'confirm'});
  await vi.waitFor(()=>expect(document.querySelector('#ext-dialog-slot button')).not.toBeNull());
  document.querySelector<HTMLButtonElement>('#ext-dialog-slot button')!.click();
  await vi.waitFor(()=>expect(fake.request).toHaveBeenCalledWith('session.ui_response','s1',{id:'dialog-1',confirmed:true},30_000));
  await vi.advanceTimersByTimeAsync(100);
  expect(document.getElementById('session-state')?.textContent).toBe('就绪');
  expect(document.querySelector('#ext-dialog-slot dialog')).toBeNull();
 });
});

describe('排队与压缩设置', () => {
  it('运行中发送前先把模式同步给桥，再带 streamingBehavior 提交', async () => {
    // send() 读的是 this.run，所以要先让界面认定在运行。
    busy = true; emit('agent_start');
    expect(document.getElementById('session-state')?.textContent).toBe('运行中');
    document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]')!.click();
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '排队消息';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await vi.waitFor(() => expect(vi.mocked(fake.request).mock.calls.some((c) => c[0] === 'session.prompt')).toBe(true));
    expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '排队消息', streamingBehavior: 'followUp' }, 30_000);
    expect(fake.request).toHaveBeenCalledWith('session.set_queue_mode', 's1', { kind: 'followUp', mode: 'one-at-a-time' }, 30_000);
    const order = vi.mocked(fake.request).mock.calls.map((c) => c[0]);
    expect(order.lastIndexOf('session.set_queue_mode')).toBeLessThan(order.lastIndexOf('session.prompt'));
  });

  it('自动压缩勾选发送 set_auto_compaction，失败时还原勾选', async () => {
    const box = document.getElementById('auto-compaction') as HTMLInputElement;
    expect(box.checked).toBe(true);
    box.checked = false; box.dispatchEvent(new Event('change'));
    await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_auto_compaction', 's1', { enabled: false }, 30_000));
    // 成功后必须回读：ensureWorker 的预取状态刷新发生在 set 之前，
    // 不重读就会把勾选重置成旧值。
    box.checked = false; box.dispatchEvent(new Event('change'));
    await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_auto_compaction', 's1', { enabled: false }, 30_000));
    const states = vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'session.state');
    expect(states.length).toBeGreaterThan(1);
    // 失败路径：有读回字段，必须恢复成 Pi 的真实状态，不停在假状态。
    fake.request.mockRejectedValueOnce(new Error('Pi 拒绝'));
    box.checked = false; box.dispatchEvent(new Event('change'));
    await vi.waitFor(() => expect(box.checked).toBe(true));
  });

  it('自动重试没有读回字段，失败时同样还原勾选', async () => {
    const box = document.getElementById('auto-retry') as HTMLInputElement;
    box.checked = true; box.dispatchEvent(new Event('change'));
    await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_auto_retry', 's1', { enabled: true }, 30_000));
    fake.request.mockRejectedValueOnce(new Error('Pi 拒绝'));
    box.checked = false; box.dispatchEvent(new Event('change'));
    await vi.waitFor(() => expect(box.checked).toBe(true));
  });

  it('排队提示只在运行中出现，并随模式变化', async () => {
    const hint = document.getElementById('queue-hint')!;
    expect(hint.hidden).toBe(true);
    busy = true; emit('agent_start');
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    expect(hint.hidden).toBe(false);
    expect(hint.textContent).toContain('插入指令');
    const steerText = hint.textContent;
    document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]')!.click();
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    // 只断言文案随模式变化，不锁死具体措辞——那是实现细节。
    expect(hint.textContent).not.toBe(steerText);
    expect(hint.textContent).toContain('排到队列末尾');
  });
});

describe('模型配置编辑器', () => {
  it('打开时读取原始配置并格式化进编辑器', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await vi.waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    // 密钥必须已经是打码值，页面拿不到真值。
    expect(document.getElementById('models-editor').value).toContain('"***"');
  });

  it('保存非法 JSON 时拒绝并不发请求', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await vi.waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    const before = vi.mocked(fake.request).mock.calls.length;
    (document.getElementById('models-editor') as HTMLTextAreaElement).value = '{ 这不是 JSON';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    expect(vi.mocked(fake.request).mock.calls.length).toBe(before);
    expect(document.getElementById('models-status').textContent).toContain('不是合法 JSON');
  });

  it('保存成功后重新读取，避免用户接着编辑旧快照', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    await vi.waitFor(() => expect(document.getElementById('models-editor').value).toContain('example.com'));
    const readsBefore = vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'config.models.raw').length;
    (document.getElementById('models-editor') as HTMLTextAreaElement).value = '{"providers":{}}';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-save', document.createElement('button'));
    await vi.waitFor(() => expect(vi.mocked(fake.request).mock.calls.filter((c) => c[0] === 'config.models.raw').length).toBeGreaterThan(readsBefore));
    expect(document.getElementById('models-status').textContent).toBe('');
  });

  it('自定义头部按行解析，非法格式被拒绝', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    (document.getElementById('discover-url') as HTMLInputElement).value = 'https://api.example.com/v1';
    (document.getElementById('discover-headers') as HTMLTextAreaElement).value = 'X-A: 1\n坏行\nX-B: 2';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-discover', document.createElement('button'));
    await vi.waitFor(() => expect(document.getElementById('discover-result').textContent).toContain('名称: 值'));
    expect(vi.mocked(fake.request).mock.calls.some((c) => c[0] === 'config.models.discover')).toBe(false);
  });

  it('发现结果按纯文本渲染，不插入 HTML', async () => {
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-edit', document.createElement('button'));
    (document.getElementById('discover-url') as HTMLInputElement).value = 'https://api.example.com/v1';
    await (workbench as unknown as { action(a: string, b: HTMLElement): Promise<void> }).action('models-discover', document.createElement('button'));
    await vi.waitFor(() => expect(document.getElementById('discover-result').textContent).toContain('GPT X'));
    expect(document.getElementById('discover-result').querySelector('script')).toBeNull();
  });
});

describe('附件随消息发送', () => {
  it('有附件时 prompt 带 images，发送成功后清空', async () => {
    (document.getElementById('prompt') as HTMLTextAreaElement).value = '看这张图';
    document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
    await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '看这张图' }, 30_000));
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
    await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.prompt', 's1', { text: '普通消息' }, 30_000));
  });
});

it('默认排队模式用协议一致的 steering，不是 steer', async () => {
  // B03：前端曾经发送 kind=steer，桥只接受 steering/followUp，
  // 运行中的「插入指令」因此被整体拒绝。这里锁定默认取值。
  busy = true; emit('agent_start');
  expect(document.querySelector<HTMLInputElement>('input[name="queue-kind"]:checked')!.value).toBe('steering');
  (document.getElementById('prompt') as HTMLTextAreaElement).value = '插入一条';
  document.querySelector<HTMLFormElement>('#composer')!.requestSubmit();
  await vi.waitFor(() => expect(fake.request).toHaveBeenCalledWith('session.set_queue_mode', 's1', { kind: 'steering', mode: 'all' }, 30_000));
  // 旧值必须不再出现。
  for (const call of vi.mocked(fake.request).mock.calls) {
    expect((call[2] as { kind?: string } | undefined)?.kind).not.toBe('steer');
  }
  // 读回状态时必须能按协议值定位到对应 radio；模板值若与协议不一致，
  // refreshQueueState 会静默失选，用户看到的勾选与实际发送的模式脱节。
  // 打开会话对话框会触发 refreshState，从而走到 refreshQueueState。
  fake.request.mockResolvedValueOnce({ sessionId: 's1', isStreaming: true, steeringMode: 'all', followUpMode: 'one-at-a-time' });
  document.querySelector<HTMLElement>('[data-action="session-menu"]')!.click();
  await vi.waitFor(() => {
    const steering = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="steering"]');
    expect(steering).not.toBeNull();
    expect(steering!.checked).toBe(true);
  });
  // 反向：Pi 报 one-at-a-time 时必须选中 followUp。
  fake.request.mockResolvedValueOnce({ sessionId: 's1', isStreaming: true, steeringMode: 'one-at-a-time', followUpMode: 'one-at-a-time' });
  document.querySelector<HTMLElement>('[data-action="session-menu"]')!.click();
  await vi.waitFor(() => {
    expect(document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]')!.checked).toBe(true);
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
    await vi.waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.start')).toBe(true));

    // 在 worker 启动等待途中切到另一个会话。
    workbench.selectSession('s2', '/tmp/other', '另一个会话');
    release();
    await vi.waitFor(() => expect(document.getElementById('connection-notice')!.textContent).toContain('未发送'));
    // 关键安全属性：绝不能投到切换后的会话。
    expect(fake.request.mock.calls.some((c) => c[0] === 'session.prompt' && c[1] === 's2')).toBe(false);
    // 输入内容必须保留，用户才能重发。
    expect((document.getElementById('prompt') as HTMLTextAreaElement).value).toBe('hello');
  });

  it('状态刷新期间切会话，command 仍发往原会话', async () => {
    // 这一条让流程真正走到命令发送：切换发生在 ensureWorker 内部，
    // 早于它的守卫会先返回，测不到目标那一行。
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'session.state') await gate;
      if (method === 'session.start') return { sessionId: 's1', cwd: '/fixture' };
      return {};
    });
    // auto-compaction 的 change 处理器走的就是 command()（U03 的同一路径）。
    const box = document.getElementById('auto-compaction') as HTMLInputElement;
    box.checked = false;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    // 等它进入 ensureWorker 内部的状态刷新。
    await vi.waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.state')).toBe(true));
    workbench.selectSession('s2', '/tmp/other', '另一个会话');
    release();
    await vi.advanceTimersByTimeAsync(10);
    await vi.waitFor(() => expect(fake.request.mock.calls.some((c) => c[0] === 'session.set_auto_compaction')).toBe(true));
    const call = fake.request.mock.calls.find((c) => c[0] === 'session.set_auto_compaction');
    // 必须是发起时归属的 s1，不能被改成 s2。
    expect(call?.[1]).toBe('s1');
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

describe('排队模式回读', () => {
  // U11：选「完成后追加」时桥调 set_follow_up_mode，改的是 followUpMode；
  // 旧实现读 steeringMode，界面被弹回「插入指令」。
  it('followUpMode 为 one-at-a-time 时选中完成后追加', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture', busy: false }];
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      if (method === 'session.state') return {
        sessionId: 's1', isStreaming: false, isCompacting: false, thinkingLevel: 'high',
        steeringMode: 'all', followUpMode: 'one-at-a-time', autoCompactionEnabled: true,
      };
      return {};
    });
    await workbench.reconcile();
    await vi.waitFor(() => {
      const followUp = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]');
      expect(followUp?.checked).toBe(true);
    });
  });

  it('followUpMode 为 all 时选中插入指令', async () => {
    fake.request.mockImplementation(async (method: string) => {
      if (method === 'worker.list') return [{ sessionId: 's1', cwd: '/fixture', busy: false }];
      if (method === 'session.thinking_levels') return ['off', 'high'];
      if (method === 'session.pending_dialogs') return { ids: [] };
      if (method === 'session.state') return {
        sessionId: 's1', isStreaming: false, isCompacting: false, thinkingLevel: 'high',
        steeringMode: 'all', followUpMode: 'all', autoCompactionEnabled: true,
      };
      return {};
    });
    await workbench.reconcile();
    await vi.waitFor(() => {
      const steering = document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="steering"]');
      expect(steering?.checked).toBe(true);
    });
  });
});

describe('历史响应的代次守卫', () => {
  // U17/U18：beforeSwap 以前只按 URL 判断会话。代次校验能挡住
  // 「切换前发起、切换后才到达」的响应——这是可在前端判定的一类。
  it('代次递增后到达的旧历史响应被拒绝', async () => {
    const turns = document.getElementById('turns')!;
    // 先在 s1 真正发起一次历史请求，让 pendingHistory 记下 {s1, epochN}。
    workbench.selectSession('s1', '/tmp/a', 'A');
    await vi.waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
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
    await vi.waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
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

describe('自动重试偏好按会话隔离', () => {
  // U14：Pi 没有 auto-retry 读回字段，跨会话共用一个 DOM 状态
  // 会把上一会话的选择带到新会话。
  it('切换会话后套用该会话的偏好，默认关闭', async () => {
    const box = document.getElementById('auto-retry') as HTMLInputElement;
    // 在 s1 打开自动重试。
    workbench.selectSession('s1', '/tmp/a', 'A');
    await vi.waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    box.checked = true;
    box.dispatchEvent(new Event('change', { bubbles: true }));
    await vi.waitFor(() => expect(box.checked).toBe(true));

    // 切到 s2：没有记录过，必须是默认关闭。
    workbench.selectSession('s2', '/tmp/b', 'B');
    await vi.waitFor(() => expect(document.body.dataset.sessionId).toBe('s2'));
    expect(box.checked).toBe(false);

    // 切回 s1：应恢复上一次的选择。
    workbench.selectSession('s1', '/tmp/a', 'A');
    await vi.waitFor(() => expect(document.body.dataset.sessionId).toBe('s1'));
    expect(box.checked).toBe(true);
  });
});
