import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { TerminalView } from '@/modules/terminal';
import type { BridgeClient } from '@/modules/bridge';
const fake=vi.hoisted(()=>({input:undefined as ((value:string)=>void)|undefined,dispose:vi.fn(),options:{} as {disableStdin?:boolean}}));
vi.mock('@xterm/xterm',()=>({Terminal:class {cols=80;rows=24;options=fake.options;loadAddon(){}open(){}focus(){}onData(fn:(value:string)=>void){fake.input=fn;return{dispose(){}};}write(_text:string,done:()=>void){done();}dispose(){fake.dispose();}}}));
vi.mock('@xterm/addon-fit',()=>({FitAddon:class{fit(){}}}));
let client:{connected:boolean;request:ReturnType<typeof vi.fn>};let view:TerminalView;
beforeEach(async()=>{
 vi.useFakeTimers();fake.options={};document.body.innerHTML='<div id=panel-terminal><div id=terminal-mount></div></div><div id=terminal-notice></div>';
 vi.stubGlobal('ResizeObserver',class{observe(){}disconnect(){}});
 client={connected:true,request:vi.fn(async(method:string)=>method==='terminal.open'?{terminalId:'terminal-1'}:{})};
 view=new TerminalView(client as unknown as BridgeClient,vi.fn());await view.open('/fixture');
});
afterEach(()=>{view.dispose();vi.useRealTimers();vi.unstubAllGlobals();});
describe('终端资源与输入',()=>{
 it('合并连续按键，不为每个字符创建并发请求',async()=>{
  for(const character of 'echo terminal-ok')fake.input!(character);
  await vi.advanceTimersByTimeAsync(15);
  const calls=client.request.mock.calls.filter(([method])=>method==='terminal.input');
  expect(calls).toHaveLength(1);expect(calls[0]?.[2]).toEqual({terminalId:'terminal-1',data:'echo terminal-ok'});
 });
 it('断线时暂停输入，不能宣称服务端资源已释放',async()=>{
  client.connected=false;view.disconnected();fake.input!('exit\n');
  await vi.advanceTimersByTimeAsync(15);
  expect(fake.options.disableStdin).toBe(true);
  await expect(view.close()).rejects.toThrow('无法确认终端关闭');
  expect(client.request.mock.calls.filter(([method])=>method==='terminal.input')).toHaveLength(0);
  expect(fake.dispose).not.toHaveBeenCalled();
 });
 it('收到关闭确认后释放渲染器',async()=>{
  await view.close();expect(client.request).toHaveBeenCalledWith('terminal.close','',{terminalId:'terminal-1'});
  expect(fake.dispose).toHaveBeenCalledTimes(1);expect(document.getElementById('terminal-notice')?.textContent).toContain('资源已释放');
 });
});
