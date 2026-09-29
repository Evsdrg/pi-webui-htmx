import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { ModelsEditor } from '@/modules/models';
import type { BridgeClient } from '@/modules/bridge';
import { readFileSync } from 'node:fs';
let editor: ModelsEditor;
let writes: any[];
const doc = {providers:{a:{baseUrl:'https://a.test',models:[{id:'m1',name:'one'},{id:'m2',name:'two'}]},b:{baseUrl:'https://b.test',models:[{id:'other'}]}}};
const input=(id:string)=>document.getElementById(id) as HTMLInputElement;
beforeEach(async()=>{
 const shell=readFileSync('src/templates/shell.html','utf8');
 document.body.innerHTML=shell.slice(shell.indexOf('<dialog id="models-dialog"'),shell.indexOf('<dialog id="branch-dialog"'));
 writes=[];
 editor=new ModelsEditor({request:async(method:string,_session:string,args:any)=>{if(method==='config.models.raw')return structuredClone(doc);writes.push(args.config);return {};}} as unknown as BridgeClient,()=>{});
 await editor.open();
});
afterEach(()=>{vi.restoreAllMocks();document.body.replaceChildren();});
it('切换节点保留字段并同步JSON草稿',()=>{
 editor.selectModel('a',0);input('mm-name').value='draft';editor.selectModel('a',1);editor.selectModel('a',0);
 expect(input('mm-name').value).toBe('draft');editor.select('json');expect(input('models-editor').value).toContain('draft');
});
it('供应商改名冲突不写盘',async()=>{
 editor.selectProvider('a');input('mp-name').value='b';await editor.save();
 expect(writes).toHaveLength(0);expect(document.getElementById('models-status')!.textContent).toContain('已存在');
});
it('反复选择模型不增加固定容器监听',()=>{
 const spy=vi.spyOn(document.getElementById('mm-thinking')!,'addEventListener');
 for(let i=0;i<5;i++)editor.selectModel('a',i%2);
 expect(spy.mock.calls.filter(x=>x[0]==='click').length).toBeLessThanOrEqual(1);
});
it('删除最后供应商可保存空文档',async()=>{
 editor.selectProvider('a');editor.deleteProvider();editor.selectProvider('b');editor.deleteProvider();await editor.save();
 expect(writes[0].providers).toEqual({});
});
it('保存期间新JSON不被覆盖且并发保存被合并',async()=>{
 let release!:()=>void;const waiting=new Promise<void>(r=>release=r);let count=0;
 editor=new ModelsEditor({request:async(method:string)=>{if(method==='config.models.raw')return structuredClone(doc);count++;await waiting;return {};}} as unknown as BridgeClient,()=>{});
 await editor.open();editor.select('json');const saving=editor.save();input('models-editor').value='{"providers":{},"newDraft":true}';await editor.save();release();await saving;
 expect(input('models-editor').value).toContain('newDraft');expect(count).toBe(1);
});
it('供应商名可用JSON普通键而不触发对象原型写入',async()=>{
 editor.selectProvider('a');input('mp-name').value='__proto__';await editor.save();
 expect(Object.hasOwn(writes[0].providers,'__proto__')).toBe(true);expect(writes[0].providers.b.models[0].id).toBe('other');
});
it('JSON导航有选中语义',()=>{editor.select('json');expect(document.querySelector('[data-models-section="json"]')!.getAttribute('aria-current')).toBe('page');});
it('未修改能力和未来映射字段不被抹掉',async()=>{
 editor.select('json');input('models-editor').value=JSON.stringify({providers:{a:{models:[{id:'m',input:['audio'],reasoning:false,thinkingLevelMap:{future:'custom',off:''}}]}}});
 editor.selectModel('a',0);await editor.save();
 expect(writes[0].providers.a.models[0]).toMatchObject({input:['audio'],reasoning:false,thinkingLevelMap:{future:'custom',off:''}});
});
