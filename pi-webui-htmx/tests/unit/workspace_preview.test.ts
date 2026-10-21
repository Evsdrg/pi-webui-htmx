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
 // 代码/纯文本产出 #file-content；Markdown 产出渲染后的 .markdown（见下方用例）。
 const isMarkdown = path.endsWith('.md');
 await vi.waitFor(()=>expect(isMarkdown
   ? document.querySelector('#panel-preview .markdown')
   : document.getElementById('file-content')).toBeTruthy());
 return document.querySelector('#panel-preview pre');
}
// Markdown 已改走渲染（下方用例覆盖），不再有 <pre> 软换行这回事。
it('纯文本预览软换行',async()=>{ expect((await open('/notes/a.txt'))?.classList.contains('wrap')).toBe(true); });
it('代码文件预览不软换行（保持横向滚动）',async()=>{ expect((await open('/src/main.go'))?.classList.contains('wrap')).toBe(false); });
it('纯文本预览软换行',async()=>{ expect((await open('/notes/a.txt'))?.classList.contains('wrap')).toBe(true); });
it('代码文件预览不软换行（保持横向滚动）',async()=>{ expect((await open('/src/main.go'))?.classList.contains('wrap')).toBe(false); });

// Markdown 文件要**渲染**，不是按代码高亮显示原文。
//
// 曾经的实现：languageFor('.md') 返回 "markdown"，于是走 hljs 把原文
// 套一层等宽字体高亮——界面上看到的是 `# 标题`、`| 指标 | 数值 |` 这种
// 原始记号。用户点开一个 md 文档期望的是读到的排版，不是源码。
//
// 与对话里的 markdown 共用同一套管线（marked + DOMPurify），
// 不额外引入解析器，也继承同样的白名单：预览的是工作区里的文件，
// 可能来自不可信来源，不能带进可触发桥命令的控件。
async function openMarkdown(source: string): Promise<HTMLElement> {
  document.body.innerHTML = '<aside id="sidebar"></aside><aside id="workspace-panel"></aside><span id="file-name"></span><div id="panel-preview" hidden></div><button id="tab-preview"></button><div id="workbench"></div>' + `<aside id="file-content"></aside>`;
  vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(String(url).includes('ui/file-image')
    ? new Response('', { status: 404 })
    : new Response(source))));
  vi.stubGlobal('htmx', { trigger: vi.fn(), ajax: vi.fn() });
  const bridge = Object.assign(new EventTarget(), { request: vi.fn().mockResolvedValue({ text: '', truncated: false }) });
  workspace = new Workspace(bridge as unknown as BridgeClient, vi.fn());
  document.getElementById('sidebar')!.innerHTML = '<button data-file-path="/docs/plan.md" data-directory="false">plan</button>';
  document.querySelector<HTMLButtonElement>('[data-file-path]')!.click();
  await vi.waitFor(() => expect(document.querySelector('#panel-preview .markdown')).toBeTruthy());
  return document.querySelector<HTMLElement>('#panel-preview .markdown')!;
}

it('Markdown 预览渲染成 DOM 而不是代码高亮', async () => {
  const node = await openMarkdown('# 标题\n\n- 第一项\n- 第二项\n');
  await vi.waitFor(() => expect(node.querySelector('h1')?.textContent).toBe('标题'));
  expect(node.querySelectorAll('li')).toHaveLength(2);
  // 不能留下代码块形态：那是"当源码看"的旧行为。
  expect(document.querySelector('#panel-preview pre code')).toBeNull();
});

it('Markdown 预览的表格渲染成表格', async () => {
  const node = await openMarkdown('| 指标 | 数值 |\n|---|---|\n| 行数 | 6922 |\n');
  await vi.waitFor(() => expect(node.querySelector('table')).toBeTruthy());
  expect(node.querySelectorAll('th')).toHaveLength(2);
  expect(node.querySelectorAll('td')).toHaveLength(2);
});

it('Markdown 预览剔除可执行/可触发表单的标记', async () => {
  const node = await openMarkdown('# 标题\n\n<script>alert(1)</script>\n\n<form><input name="x"></form>\n\n<button>点我</button>\n');
  await vi.waitFor(() => expect(node.querySelector('h1')).toBeTruthy());
  expect(node.querySelector('script')).toBeNull();
  expect(node.querySelector('form')).toBeNull();
  expect(node.querySelector('input')).toBeNull();
  expect(node.querySelector('button')).toBeNull();
});
