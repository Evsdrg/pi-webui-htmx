import { afterEach, expect, it, vi } from 'vitest';
import { Workspace } from '@/modules/workspace';
import type { BridgeClient } from '@/modules/bridge';
let workspace:Workspace;
afterEach(()=>{workspace?.dispose();vi.unstubAllGlobals();document.body.replaceChildren();});
it('文件原文迟到不能在关闭后重新打开预览',async()=>{
 document.body.innerHTML='<aside id="sidebar"></aside><aside id="workspace-panel"><button data-action="file-close">关闭</button></aside><span id="file-name"></span><div id="panel-preview" hidden><div id="file-content"></div></div>';
 let finish!:(r:Response)=>void;const request=new Promise<Response>(r=>finish=r);vi.stubGlobal('fetch',vi.fn(()=>request));
 vi.stubGlobal('htmx',{trigger:vi.fn(),ajax:vi.fn()});
 const bridge=Object.assign(new EventTarget(),{request:vi.fn().mockResolvedValue({binary:false})});workspace=new Workspace(bridge as unknown as BridgeClient,vi.fn());
 document.getElementById('sidebar')!.innerHTML='<button data-file-path="/old.txt" data-directory="false">旧文件</button>';
 document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();await vi.waitFor(()=>expect(fetch).toHaveBeenCalledOnce());
 document.querySelector<HTMLButtonElement>('[data-action=file-close]')!.click();finish(new Response('old content'));
 await new Promise(r=>setTimeout(r,0));expect(document.getElementById('panel-preview')!.hidden).toBe(true);expect(document.getElementById('file-content')!.textContent).toBe('');
});
