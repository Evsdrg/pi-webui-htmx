import { expect, it, vi } from 'vitest';
import { mountFragmentRequests } from '@/modules/fragment-requests';
it('旧xhr在A到B到A后不能处理响应头或OOB',()=>{
 let epoch=0;const abort=new AbortController();mountFragmentRequests(()=>epoch,abort.signal);
 const target=document.createElement('div');target.id='turns';document.body.append(target);
 const old={abort:vi.fn()};document.dispatchEvent(new CustomEvent('htmx:beforeRequest',{detail:{target,xhr:old}}));
 epoch=2;const next={abort:vi.fn()};document.dispatchEvent(new CustomEvent('htmx:beforeRequest',{detail:{target,xhr:next}}));
 const response=new CustomEvent('htmx:beforeOnLoad',{cancelable:true,detail:{xhr:old}});document.dispatchEvent(response);
 expect(response.defaultPrevented).toBe(true);expect(old.abort).toHaveBeenCalledOnce();
 const current=new CustomEvent('htmx:beforeOnLoad',{cancelable:true,detail:{xhr:next}});document.dispatchEvent(current);expect(current.defaultPrevented).toBe(false);
 abort.abort();target.remove();
});
it('目录输入或模型选择变更使在途片段失效',()=>{
 const abort=new AbortController();mountFragmentRequests(()=>0,abort.signal);const target=document.createElement('div');target.id='dir-list';document.body.append(target);
 const xhr={abort:vi.fn()};document.dispatchEvent(new CustomEvent('htmx:beforeRequest',{detail:{target,xhr}}));target.dataset.requestScope='new';
 const response=new CustomEvent('htmx:beforeOnLoad',{cancelable:true,detail:{xhr}});document.dispatchEvent(response);expect(response.defaultPrevented).toBe(true);abort.abort();target.remove();
});
