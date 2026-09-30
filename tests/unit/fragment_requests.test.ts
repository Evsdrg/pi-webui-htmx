import { describe, expect, it, vi } from 'vitest';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
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

// B54 的设备前缀适配：所有**发起请求**的入口都必须是相对路径。
// 契约脚本按模板查，这条按代码查——JS 里的 location.assign 不受
// <base href> 影响，写根绝对路径在云端会跳出设备前缀。
describe('请求入口都用相对路径', () => {
  it('导出下载通过文档基地址解析', async () => {
    const source = await readFile(resolve(process.cwd(), 'src/modules/workbench.ts'), 'utf8');
    const calls = [...source.matchAll(/location\.assign\(([^)]*)\)/g)].map((m) => m[1]);
    expect(calls.length).toBeGreaterThan(0);
    for (const call of calls) {
      expect(call).not.toMatch(/^`\//);
      expect(call).not.toMatch(/^'\/"/);
      expect(call).toContain('absoluteUrl');
    }
  });
});
