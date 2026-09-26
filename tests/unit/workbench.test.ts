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
const methods = ['session.start','session.prompt','session.subscribe','worker.list','session.state','session.thinking_levels','session.pending_dialogs','session.ui_response','session.stats','session.set_queue_mode','session.set_auto_compaction','session.set_auto_retry','session.abort_retry','session.export_html'];
function emit(type: string, extra: Record<string, unknown> = {}) {
 fake.instance!.dispatchEvent(new CustomEvent('message', { detail: { version:1,kind:'event',event:'pi.event',sessionId:'s1',epoch:'test',seq:++sequence,data:{type,...extra} } }));
}
function mount() {
 document.body.innerHTML = `<form id=auth-form><input id=bridge-token><button>连接</button></form><dialog id=auth-dialog></dialog><div id=auth-error></div>
 <form id=composer><textarea id=prompt></textarea><button id=send-button></button><button id=abort-button></button><select id=model-select></select><select id=thinking-select></select></form>
 <form id=new-form><input id=cwd-input></form><dialog id=new-dialog></dialog><datalist id=workspace-roots></datalist><input id=session-search>
 <dialog id=session-dialog><input id=session-name><div class=session-action-grid><button data-action=rename>保存名称</button><button data-action=compact>压缩</button><button data-action=clone>克隆</button><button data-action=export>导出</button><button data-action=stop>释放</button><button data-action=delete>删除</button></div>
 <label class=switch><input type=checkbox id=auto-compaction><span>自动压缩</span></label><label class=switch><input type=checkbox id=auto-retry><span>自动重试</span></label>
 <button data-action=abort-retry>中止重试</button>
 <fieldset class=queue-modes><label class=switch><input type=radio name=queue-kind value=steer checked><span>插入指令</span></label><label class=switch><input type=radio name=queue-kind value=followUp><span>完成后追加</span></label></fieldset></dialog>
 <div class=queue-hint id=queue-hint hidden></div>`;
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
    document.querySelector<HTMLInputElement>('input[name="queue-kind"][value="followUp"]')!.click();
    document.getElementById('prompt')!.dispatchEvent(new Event('input'));
    expect(hint.textContent).toContain('排到队列末尾');
  });
});
