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

// 右栏预览的换行：散文（Markdown / 纯文本）软换行，代码与数据保持横向滚动。
// 曾经的 bug：样式选择器写成早已不存在的 #file-preview，预览的 <pre> 连
// overflow 都没有，长行直接横向溢出、Markdown 读不了。
async function open(path:string):Promise<HTMLElement|null>{
 document.body.innerHTML='<aside id="sidebar"></aside><aside id="workspace-panel"></aside><span id="file-name"></span><div id="panel-preview" hidden></div><button id="tab-preview"></button><div id="workbench"></div>';
 vi.stubGlobal('fetch',vi.fn((url:string)=>Promise.resolve(String(url).includes('ui/file-image')
   ? new Response('',{status:404})                     // 非图片：走文本分支
   : new Response('# 标题\n这是一段足够长的正文用来验证软换行可以生效'))));
 vi.stubGlobal('htmx',{trigger:vi.fn(),ajax:vi.fn()});
 const bridge=Object.assign(new EventTarget(),{request:vi.fn().mockResolvedValue({text:'',truncated:false})});
 workspace=new Workspace(bridge as unknown as BridgeClient,vi.fn());
 document.getElementById('sidebar')!.innerHTML=`<button data-file-path="${path}" data-directory="false">file</button>`;
 document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
 await vi.waitFor(()=>expect(document.getElementById('file-content')).toBeTruthy());
 return document.querySelector('#panel-preview pre');
}
it('Markdown 预览软换行',async()=>{ expect((await open('/docs/README.md'))?.classList.contains('wrap')).toBe(true); });
it('纯文本预览软换行',async()=>{ expect((await open('/notes/a.txt'))?.classList.contains('wrap')).toBe(true); });
it('代码文件预览不软换行（保持横向滚动）',async()=>{ expect((await open('/src/main.go'))?.classList.contains('wrap')).toBe(false); });
